"""Fixed, root-operated Foundry lifecycle. No AWS calls or installer actions."""
import configparser
from contextlib import contextmanager
import fcntl
import grp
import hashlib
import json
import os
from pathlib import Path
import platform
import pwd
import re
import shutil
import subprocess
import sys
import tempfile
import time

CONFIG = Path('/etc/foundry/host.json')
MOUNT = Path('/foundry')
APP = MOUNT / 'foundryvtt'
DATA = MOUNT / 'foundrydata'
TLS = Path('/etc/foundry/tls')
LINEAGE = Path('/etc/letsencrypt/live/foundry.craigdevjohnson.com')
HOSTNAME = 'foundry.craigdevjohnson.com'
UNIT = 'foundry.service'
CGROUP = Path('/sys/fs/cgroup/system.slice/foundry.service')
PROC = Path('/proc')
LOCK = Path('/run/lock/foundry-lifecycle.lock')
CERT_LOCK = Path('/run/lock/foundry-certificate.lock')
MIN_CERT_SECONDS = 86400


class Failure(Exception):
    pass


class Busy(Failure):
    pass


class RenewalUnavailable(Failure):
    """Certbot could not complete renewal after its configuration was validated."""


def run(args, timeout=15):
    try:
        result = subprocess.run(args, capture_output=True, text=True, timeout=timeout,
                                env={'PATH': '/usr/sbin:/usr/bin:/sbin:/bin', 'LC_ALL': 'C'})
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise Failure(f'Command did not complete: {args[0]}: {exc}') from exc
    if result.returncode:
        raise Failure(f'{args[0]} failed ({result.returncode}): {result.stderr.strip()}')
    return result.stdout.strip()


def digest(path):
    checksum = hashlib.sha256()
    with open(path, 'rb') as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b''):
            checksum.update(chunk)
    return checksum.hexdigest()


def process_identity(process):
    # comm may contain spaces or parentheses; the remaining fields start at state.
    prefix, separator, tail = (process / 'stat').read_text().rpartition(') ')
    fields = tail.split()
    if not separator or int(prefix.split(' ', 1)[0]) != int(process.name):
        raise ValueError('Invalid process stat identity')
    return fields[0], int(fields[6]), int(fields[19])  # state, flags, start time


def uses_foundry_paths(args, cwd, executable):
    # procfs marks unlinked paths while their processes can still hold data open.
    cwd = Path(str(cwd).removesuffix(' (deleted)'))
    executable = Path(str(executable).removesuffix(' (deleted)'))
    node = executable.name in {'node', 'nodejs'}
    paths = [executable, cwd] if node else [executable]
    for index, arg in enumerate(args):
        data_arg = arg.startswith('--dataPath=') or (index > 0 and args[index - 1] == '--dataPath')
        value = arg.split('=', 1)[1] if arg.startswith('--') and '=' in arg else arg
        if value and not value.startswith('-') and (node or data_arg or value.startswith(('/', './', '../'))):
            paths.append(Path(os.path.realpath(cwd / value)))
    return (cwd == DATA or DATA in cwd.parents
            or executable.name.casefold() in {'foundry', 'foundryvtt'}
            or any(root == path or root in path.parents
                   for path in paths for root in (APP, DATA))
            or any('foundryvtt' in (part.casefold() for part in path.parts) for path in paths))


def validate_manifest(value):
    """A partial baseline must never produce an installable configuration."""
    example = {
        'schema', 'setup_revision', 'os', 'mount_uuid', 'uid', 'gid', 'node_version',
        'node_binary_sha256', 'node_archive_sha256', 'foundry_version',
        'foundry_main_sha256', 'foundry_archive_sha256', 'worlds', 'rpms',
    }
    if (not isinstance(value, dict) or set(value) != example
            or type(value['schema']) is not int or value['schema'] != 1):
        raise Failure('Unsupported or incomplete host manifest')
    for key in ('node_binary_sha256', 'node_archive_sha256', 'foundry_main_sha256',
                'foundry_archive_sha256'):
        if not re.fullmatch(r'[0-9a-f]{64}', str(value[key])):
            raise Failure(f'Missing or invalid pin: {key}')
    if not re.fullmatch(r'[0-9a-f]{40}', str(value['setup_revision'])):
        raise Failure('setup_revision must be the reviewed full Git commit')
    if value['node_version'] != '24.18.1' or value['foundry_version'] != '14.366.0':
        raise Failure('Adoption must preserve Node 24.18.1 and Foundry 14.366.0')
    os_pin = value['os']
    if not isinstance(os_pin, dict) or set(os_pin) != {'ami_id', 'architecture', 'system_release'}:
        raise Failure('Incomplete OS pin')
    if (os_pin['architecture'] != 'aarch64'
            or not re.fullmatch(r'ami-[0-9a-f]{17}', str(os_pin['ami_id']))
            or not re.fullmatch(r'Amazon Linux release 2023\.[0-9.]+ \(Amazon Linux\)',
                                str(os_pin['system_release']))):
        raise Failure('Pin the ARM64 AMI and exact Amazon Linux 2023 system release')
    if not re.fullmatch(r'[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}', str(value['mount_uuid'])):
        raise Failure('Pin the ext4 filesystem UUID')
    if any(type(value[k]) is not int or value[k] < 1 for k in ('uid', 'gid')):
        raise Failure('Pin non-root foundry UID and GID')
    worlds = value['worlds']
    if (not isinstance(worlds, list) or len(worlds) != 2 or len(set(map(str, worlds))) != 2
            or any(not isinstance(w, str) or not re.fullmatch(r'[a-z0-9][a-z0-9_-]*', w) for w in worlds)):
        raise Failure('Pin both existing world directory names')
    rpms = value['rpms']
    required = {'python3', 'systemd', 'openssl', 'certbot', 'iproute', 'util-linux', 'curl', 'ca-certificates'}
    if not isinstance(rpms, dict) or set(rpms) != required:
        raise Failure('Pin all prerequisite RPMs')
    for name, nevra in rpms.items():
        if not isinstance(nevra, str) or not re.fullmatch(re.escape(name) + r'-[A-Za-z0-9:_.+~-]+', nevra):
            raise Failure(f'Pin the exact RPM NEVRA for {name}')
    return value


@contextmanager
def locked(path=LOCK, wait=0):
    # Root-owned lock directory; no caller-selected path or inherited bypass flag.
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        deadline = time.monotonic() + wait
        while True:
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError as exc:
                if time.monotonic() >= deadline:
                    raise Busy('Another host operation is running; reconcile it before retrying') from exc
                time.sleep(0.2)
        yield
    finally:
        os.close(fd)


class Host:
    def __init__(self, manifest):
        self.manifest = validate_manifest(manifest)

    def mount(self):
        fields = run(['/usr/bin/findmnt', '--mountpoint', str(MOUNT), '--noheadings',
                      '--output', 'UUID,FSTYPE,OPTIONS']).split()
        if (len(fields) != 3 or fields[0] != self.manifest['mount_uuid']
                or fields[1] != 'ext4' or 'rw' not in fields[2].split(',')):
            raise Failure('Required writable ext4 /foundry mount UUID is missing or wrong')

    def unit_commands(self):
        # The Ex properties retain argument boundaries and the +/!/- command flags.
        # systemctl show's human-readable argv joins strings and loses that boundary.
        commands = {
            'ExecStartEx': [['/usr/bin/node',
                             ['/usr/bin/node', '/foundry/foundryvtt/main.js',
                              '--dataPath=/foundry/foundrydata', '--port=30000'], []]],
            'ExecStartPreEx': [['/usr/local/sbin/foundry-host',
                                ['/usr/local/sbin/foundry-host', 'preflight'], ['privileged']]],
            'ExecConditionEx': [], 'ExecStartPostEx': [], 'ExecReloadEx': [],
            'ExecStopEx': [], 'ExecStopPostEx': [],
        }
        properties = [*commands, 'EnvironmentFiles', 'PermissionsStartOnly']
        output = run(['/usr/bin/busctl', '--json=short', 'get-property',
                      'org.freedesktop.systemd1',
                      '/org/freedesktop/systemd1/unit/foundry_2eservice',
                      'org.freedesktop.systemd1.Service', *properties])
        try:
            values = [json.loads(line) for line in output.splitlines()]
        except ValueError as exc:
            raise Failure('Cannot verify effective systemd command contract') from exc
        if len(values) != len(properties):
            raise Failure('Incomplete effective systemd command contract')
        for name, value in zip(properties, values):
            if name in commands:
                valid = (isinstance(value, dict) and value.get('type') == 'a(sasasttttuii)'
                         and isinstance(value.get('data'), list)
                         and all(isinstance(record, list) and len(record) == 10
                                 and all(type(field) is int for field in record[3:])
                                 for record in value['data'])
                         and [record[:3] for record in value['data']] == commands[name])
            else:
                expected = ({'type': 'a(sb)', 'data': []} if name == 'EnvironmentFiles'
                            else {'type': 'b', 'data': False})
                valid = value == expected
            if not valid:
                raise Failure(f'Effective systemd command contract differs: {name}')

    def unit_contract(self):
        expected = {'User': 'foundry', 'Group': 'foundry', 'KillMode': 'control-group',
                    'KillSignal': '15', 'SendSIGKILL': 'no', 'Restart': 'no',
                    'FragmentPath': '/etc/systemd/system/foundry.service', 'DropInPaths': '',
                    'LoadState': 'loaded', 'NeedDaemonReload': 'no', 'Type': 'exec',
                    'WorkingDirectory': '/foundry/foundryvtt',
                    'TimeoutStartUSec': '10min', 'TimeoutStopUSec': '1min 30s',
                    'TimeoutStartFailureMode': 'terminate', 'TimeoutStopFailureMode': 'terminate',
                    'UMask': '0007', 'NoNewPrivileges': 'yes', 'PrivateTmp': 'yes',
                    'RemainAfterExit': 'no', 'DynamicUser': 'no', 'SupplementaryGroups': '',
                    'Environment': '', 'PassEnvironment': '', 'UnsetEnvironment': '',
                    'RootDirectory': '', 'RootImage': '', 'BindPaths': '',
                    'BindReadOnlyPaths': '', 'TemporaryFileSystem': ''}
        dependencies = {'RequiresMountsFor', 'Requires', 'After', 'Wants'}
        output = run(['/usr/bin/systemctl', 'show', UNIT, '--no-pager',
                      '--property=' + ','.join([*expected, *sorted(dependencies)])])
        fields = [line.split('=', 1) for line in output.splitlines() if '=' in line]
        actual = dict(fields)
        if (len(fields) != len(actual) or set(actual) != set(expected) | dependencies
                or any(actual[key] != value for key, value in expected.items())
                or set(actual['RequiresMountsFor'].split()) != {
                    '/foundry', '/foundry/foundryvtt', '/var/tmp'}
                or 'foundry.mount' not in actual['Requires'].split()
                or not {'foundry.mount', 'network-online.target', 'time-sync.target'}
                    <= set(actual['After'].split())
                or not {'network-online.target', 'time-sync.target'} <= set(actual['Wants'].split())):
            raise Failure('Effective systemd launch or shutdown contract differs; review unit and drop-ins')
        self.unit_commands()

    def inputs(self):
        self.mount()
        pin = self.manifest
        if (platform.machine() != pin['os']['architecture']
                or Path('/etc/system-release').read_text().strip() != pin['os']['system_release']):
            raise Failure('OS release or architecture differs from the reconstruction manifest')
        for name, expected in pin['rpms'].items():
            if run(['/usr/bin/rpm', '-q', '--qf', '%{NEVRA}', name]) != expected:
                raise Failure(f'RPM differs from pin: {name}')
        if run(['/usr/bin/node', '--version']) != 'v' + pin['node_version']:
            raise Failure('Node version differs from pin')
        for path, expected in ((Path('/usr/bin/node'), pin['node_binary_sha256']),
                               (APP / 'main.js', pin['foundry_main_sha256'])):
            if digest(path) != expected:
                raise Failure(f'Runtime hash differs from pin: {path}')
        package = json.loads((APP / 'package.json').read_text())
        if not isinstance(package, dict) or package.get('version') != pin['foundry_version']:
            raise Failure('Foundry version differs from pin')
        account = pwd.getpwnam('foundry')
        if (account.pw_uid, account.pw_gid, grp.getgrnam('foundry').gr_gid) != (pin['uid'], pin['gid'], pin['gid']):
            raise Failure('foundry account ownership differs from pin')
        for path in (MOUNT, APP):
            stat = path.stat()
            if path.is_symlink() or stat.st_uid != 0 or stat.st_mode & 0o022:
                raise Failure(f'Application parent must be root-owned and not writable by others: {path}')
        data_root = DATA.resolve(strict=True)
        for path in (DATA, DATA / 'Data', DATA / 'Data/worlds', DATA / 'Config', DATA / 'Backups',
                     *(DATA / 'Data/worlds' / world for world in pin['worlds'])):
            stat = path.stat()
            if (path.is_symlink() or not path.is_dir() or
                    (stat.st_uid, stat.st_gid) != (pin['uid'], pin['gid']) or stat.st_mode & 0o007):
                raise Failure(f'Required data directory has unsafe ownership or mode: {path}')
            if not path.resolve(strict=True).is_relative_to(data_root):
                raise Failure(f'Required data directory resolves outside the data root: {path}')
        options = json.loads((DATA / 'Config/options.json').read_text())
        expected = {'port': 30000, 'hostname': HOSTNAME,
                    'sslCert': str(TLS / 'current/fullchain.pem'),
                    'sslKey': str(TLS / 'current/privkey.pem')}
        if not isinstance(options, dict) or any(options.get(key) != val for key, val in expected.items()):
            raise Failure('Foundry options must use the fixed hostname, port and TLS paths')

    def listeners(self):
        output = run(['/usr/sbin/ss', '-H', '-ltnp', 'sport = :30000'])
        if not output:
            return set()
        pids = set()
        for line in output.splitlines():
            found = re.findall(r'pid=(\d+)', line)
            if not found:
                raise Failure('Cannot identify port 30000 listener owner')
            pids.update(map(int, found))
        return pids

    def state(self):
        output = run(['/usr/bin/systemctl', 'show', UNIT, '--no-pager',
                      '--property=LoadState,ActiveState,SubState,Result,MainPID,ControlPID,ControlGroup'])
        fields = dict(line.split('=', 1) for line in output.splitlines() if '=' in line)
        required = {'LoadState', 'ActiveState', 'SubState', 'Result', 'MainPID', 'ControlPID', 'ControlGroup'}
        if set(fields) != required or fields['LoadState'] != 'loaded':
            raise Failure('Cannot establish loaded Foundry service state')
        return fields

    def cgroup_pids(self):
        if not CGROUP.exists():
            return set()
        return {int(pid) for path in CGROUP.rglob('cgroup.procs') for pid in path.read_text().split()}

    def foundry_pids(self):
        pids = set()
        try:
            processes = list(PROC.iterdir())
        except OSError as exc:
            raise Failure('Cannot enumerate host processes') from exc
        for process in processes:
            if not process.name.isdecimal() or int(process.name) == os.getpid():
                continue
            try:
                state, flags, started = process_identity(process)
                # Zombies have exited; PF_KTHREAD identifies kernel-only tasks.
                if state in {'Z', 'X', 'x'} or flags & 0x00200000:
                    continue
                args = [os.fsdecode(arg) for arg in (process / 'cmdline').read_bytes().rstrip(b'\0').split(b'\0')]
                if not args[0]:
                    raise ValueError('Empty live userspace command line')
                cwd = Path(os.readlink(process / 'cwd'))
                executable = Path(os.readlink(process / 'exe'))
                if not cwd.is_absolute() or not executable.is_absolute():
                    raise ValueError('Invalid process path')
                if process_identity(process)[2] != started:
                    raise ValueError('Process identity changed during inspection')
            except (OSError, ValueError, IndexError) as exc:
                try:
                    process.stat()
                except FileNotFoundError:
                    continue
                except OSError:
                    pass
                raise Failure(f'Cannot inspect live process {process.name}; reconcile before proceeding') from exc
            if uses_foundry_paths(args, cwd, executable):
                pids.add(int(process.name))
        return pids

    def stopped(self):
        state = self.state()
        if (state['ActiveState'] != 'inactive' or state['SubState'] != 'dead'
                or state['Result'] != 'success' or state['MainPID'] != '0'
                or state['ControlPID'] != '0' or self.cgroup_pids() or self.listeners()
                or self.foundry_pids()):
            raise Failure('Shutdown is not verified: inspect unit result, cgroup, host processes and port 30000; do not back up or stop EC2')

    def ready(self):
        self.mount()
        self.unit_contract()
        state = self.state()
        pid = int(state['MainPID'])
        if (state['ActiveState'] != 'active' or state['SubState'] != 'running'
                or state['Result'] != 'success' or pid <= 0 or state['ControlPID'] != '0'
                or state['ControlGroup'] != '/system.slice/foundry.service'
                or self.cgroup_pids() != {pid} or self.listeners() != {pid}
                or self.foundry_pids() - {pid}):
            raise Failure('Foundry is not the single managed listener')
        process = PROC / str(pid)
        expected_args = ['/usr/bin/node', str(APP / 'main.js'),
                         '--dataPath=' + str(DATA), '--port=30000']
        status = dict(line.split(':', 1) for line in (process / 'status').read_text().splitlines() if ':' in line)
        if (os.path.realpath(process / 'exe') != os.path.realpath('/usr/bin/node')
                or (process / 'cmdline').read_bytes().rstrip(b'\0').decode().split('\0') != expected_args
                or status['Uid'].split() != [str(self.manifest['uid'])] * 4
                or status['Gid'].split() != [str(self.manifest['gid'])] * 4):
            raise Failure('Serving process does not match the pinned command and foundry identity')
        code = run(['/usr/bin/curl', '--silent', '--show-error', '--fail', '--max-time', '5',
                    '--noproxy', '*', '--resolve', f'{HOSTNAME}:30000:127.0.0.1',
                    '--output', '/dev/null', '--write-out', '%{http_code}', f'https://{HOSTNAME}:30000/'])
        if code not in {'200', '302', '303'}:
            raise Failure(f'Unexpected Foundry HTTPS response: {code}')

    def certificate(self, directory):
        cert, key = str(directory / 'fullchain.pem'), str(directory / 'privkey.pem')
        run(['/usr/bin/openssl', 'x509', '-in', cert, '-noout', '-checkend', str(MIN_CERT_SECONDS)])
        run(['/usr/bin/openssl', 'verify', '-purpose', 'sslserver', '-verify_hostname', HOSTNAME,
             '-CAfile', '/etc/pki/tls/certs/ca-bundle.crt', '-untrusted', cert, cert])
        public = run(['/usr/bin/openssl', 'x509', '-in', cert, '-pubkey', '-noout'])
        private_public = run(['/usr/bin/openssl', 'pkey', '-in', key, '-pubout'])
        if public != private_public:
            raise Failure('Certificate and private key do not match')

    def renew(self):
        config = configparser.ConfigParser(interpolation=None)
        config.read_string('[lineage]\n' + Path(
            '/etc/letsencrypt/renewal/foundry.craigdevjohnson.com.conf').read_text())
        if config.get('renewalparams', 'authenticator', fallback='') != 'standalone':
            raise Failure('Expected inventoried Certbot standalone lineage')
        if config.get('renewalparams', 'installer', fallback='none').strip().lower() not in {'', 'none'}:
            raise Failure('Certbot installer plugins must be disabled before adoption')
        if any(key.endswith('_hook') and val.strip()
               for section in config.values() for key, val in section.items()):
            raise Failure('Remove reviewed legacy Certbot hooks before adoption')
        try:
            run(['/usr/bin/certbot', '--config', '/etc/foundry/certbot.ini', 'renew',
                 '--cert-name', HOSTNAME, '--non-interactive', '--no-random-sleep-on-renew',
                 '--no-directory-hooks'], timeout=180)
        except Failure as exc:
            raise RenewalUnavailable(str(exc)) from exc
        self.certificate(LINEAGE)

    def publish_certificate(self):
        self.certificate(LINEAGE)
        TLS.mkdir(mode=0o750, parents=True, exist_ok=True)
        os.chown(TLS, 0, self.manifest['gid'])
        # Versioned pairs avoid exposing a new certificate with the previous key.
        directory = Path(tempfile.mkdtemp(prefix='pair-', dir=TLS))
        os.chown(directory, 0, self.manifest['gid'])
        directory.chmod(0o750)
        for name in ('fullchain.pem', 'privkey.pem'):
            dest = directory / name
            shutil.copyfile(LINEAGE / name, dest)
            os.chown(dest, 0, self.manifest['gid'])
            dest.chmod(0o640)
        self.certificate(directory)
        link = directory.with_name(directory.name + '-link')
        link.symlink_to(directory.name)
        os.replace(link, TLS / 'current')

    def preflight(self):
        self.inputs()
        self.unit_contract()
        if self.listeners() or self.cgroup_pids() - {os.getpid()} or self.foundry_pids():
            raise Failure('Refusing duplicate startup: listener or previous Foundry process exists')
        deadline = time.monotonic() + 60
        while run(['/usr/bin/timedatectl', 'show', '--property=NTPSynchronized', '--value'], timeout=5) != 'yes':
            if time.monotonic() >= deadline:
                raise Failure('Clock is not synchronized; retry startup after time synchronization')
            time.sleep(2)
        with locked(CERT_LOCK, wait=200):
            try:
                self.renew()
            except RenewalUnavailable as exc:
                # HTTP-01 can fail temporarily. An independently verified active pair
                # remains usable; missing/expired/untrusted pairs still block startup.
                print(f'Certificate renewal failed: {exc}', file=sys.stderr)
                self.certificate(TLS / 'current')
            else:
                self.publish_certificate()
        for name in ('fullchain.pem', 'privkey.pem'):
            run(['/usr/sbin/runuser', '-u', 'foundry', '--', '/usr/bin/test', '-r',
                 str(TLS / 'current' / name)])
        self.mount()
        if self.listeners() or self.cgroup_pids() - {os.getpid()} or self.foundry_pids():
            raise Failure('Port 30000 became occupied or a Foundry process appeared during preflight')

    def start(self):
        self.unit_contract()
        state = self.state()
        if state['ActiveState'] == 'active':
            self.ready()
            return
        self.stopped()
        run(['/usr/bin/systemctl', 'start', UNIT], timeout=610)
        deadline = time.monotonic() + 60
        while True:
            try:
                self.ready()
                return
            except Failure:
                if time.monotonic() >= deadline:
                    raise Failure('HTTPS readiness timed out; service may still be running, reconcile status')
                time.sleep(2)

    def stop(self):
        self.unit_contract()
        state = self.state()
        if state['ActiveState'] != 'inactive':
            if (state['ActiveState'] != 'active' or state['SubState'] != 'running'
                    or state['Result'] != 'success'):
                raise Failure('Service state is unresolved; inspect it before requesting another stop')
            run(['/usr/bin/systemctl', 'stop', UNIT], timeout=100)
        self.stopped()

    def restart(self):
        self.stop()
        self.start()


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv
    descriptions = {
        'start': 'Start the service and verify managed HTTPS readiness.',
        'stop': 'Stop the service and verify no writer or listener remains.',
        'restart': 'Verify shutdown before starting the service again.',
        'status': 'Read and verify the current ready or stopped state.',
        'renew': 'Fetch and validate the fixed certificate lineage without restarting Foundry.',
        'preflight': 'Internal systemd startup checks; do not expose through Automation.',
    }
    if argv in (['--help'], ['-h']) or (len(argv) == 2 and argv[0] in descriptions and argv[1] == '--help'):
        selected = {argv[0]: descriptions[argv[0]]} if len(argv) == 2 else descriptions
        print('Usage: foundry-host {start|stop|restart|status|renew|preflight}\n')
        for name, description in selected.items():
            print(f'{name}: {description}')
        print('\nExamples:\n  sudo /usr/local/sbin/foundry-host ' + (argv[0] if len(argv) == 2 else 'status'))
        print('\nExits: 0 verified success, 1 failure or uncertainty, 2 operation busy.\n'
              'Runtime commands return one JSON object. Host and paths are fixed.')
        return 0
    if len(argv) != 1 or argv[0] not in descriptions:
        print('Usage: foundry-host {start|stop|restart|status|renew|preflight}', file=sys.stderr)
        return 1
    action = argv[0]
    try:
        if os.geteuid() != 0:
            raise Failure('Run the installed command as root')
        stat = CONFIG.stat()
        if CONFIG.is_symlink() or stat.st_uid != 0 or stat.st_mode & 0o022:
            raise Failure('Manifest must be root-owned and not writable by others')
        host = Host(json.loads(CONFIG.read_text()))
        if action == 'preflight':
            host.preflight()
            outcome = 'prepared'
        elif action == 'status':
            state = host.state()
            if state['ActiveState'] == 'inactive':
                host.stopped()
                outcome = 'stopped'
            else:
                host.ready()
                outcome = 'ready'
        else:
            with locked():
                if action == 'renew':
                    with locked(CERT_LOCK):
                        host.renew()
                    outcome = 'certificate-available'
                else:
                    getattr(host, action)()
                    outcome = 'stopped' if action == 'stop' else 'ready'
        print(json.dumps({'schema': 1, 'action': action, 'outcome': outcome}))
        return 0
    except (Failure, OSError, ValueError, KeyError, configparser.Error) as exc:
        print(json.dumps({'schema': 1, 'action': action, 'outcome': 'error', 'message': str(exc)}))
        return 2 if isinstance(exc, Busy) else 1
