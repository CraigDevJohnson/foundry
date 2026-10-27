#!/usr/bin/env python3
"""Run the live checks of issue #25 on the Foundry host, from Craig's computer.

The checks need the running host and the workloads-admin profile, which only
Craig has, so this script does them in one sitting. Each step prints what it
found, and the run ends with a Markdown record you can post on the issue.

  1. Preflight: a terminal, gh signed in, and the workloads profiles giving
     credentials in account 793680745829 (it runs `aws sso login`, which opens
     a browser, when they do not).
  2. Finds the host by its tags and records its state.
  3. Starts the host with the Host control workflow (action=start), which
     also waits until Foundry answers; the script then waits for /join to
     answer 200. A host that is already running or starting is only checked,
     after you confirm nobody is playing.
  4. Reads the host through Run Command with workloads-admin: /foundry
     mounted, foundry.service active, certbot-renew.timer enabled and
     persistent with a next run, its unit files, the last runs, the deploy
     hook, and the certificate certbot holds and the one Foundry serves.
  5. If the timer is not persistent, or the deploy hook is not the link it
     should be, fixes it on the host: Persistent=true in the override,
     daemon-reload, restart the timer, relink the hook.
  6. Renews the certificate now by starting certbot-renew.service (the
     certificate has been in its renewal window since 2026-10-18). The deploy
     hook then restarts Foundry. Skip this with --no-renew.
  7. From this computer: /join answers 200, and the served certificate
     expires later than 2026-11-17.
  8. Pauses for the browser checks: both worlds open, and the GM login and a
     player join-and-save when you can. Skip the pause with --no-browser-pause.
  9. End session with the Host control workflow (action=stop, nobody
     playing) when you type "stop", then waits for EC2 to report stopped.
     Skip with --keep-running, or type "keep" at the prompt.
 10. Writes live-checks-<time>.md in the current directory and offers to post
     it as a comment on foundry#25. A run that stops early writes the record
     too, with what it found and the host's state at that point.

Run it from any directory, at a terminal:

    python3 scripts/live-checks.py

It needs python3, gh, aws, openssl and curl, and nothing else. Every command
it runs is printed first.
"""
import argparse
from datetime import date, datetime, timezone
import json
import os
import re
import shutil
import subprocess
import sys
import time

REPO = 'CraigDevJohnson/foundry'
ISSUE = 25
WORKFLOW = 'host-control.yml'
REGION = 'us-west-2'
ACCOUNT = '793680745829'
EXPECTED_INSTANCE = 'i-05dfb9abbc1caac32'
HOSTNAME = 'foundry.craigdevjohnson.com'
PORT = 30000
BASE_URL = f'https://{HOSTNAME}:{PORT}'
READ_PROFILE = 'workloads-readonly'
ADMIN_PROFILE = 'workloads-admin'
# The certificate issued on the old host expires on this day. A renewed one
# expires on a later day; the comparison is by day, never by the clock time.
CURRENT_EXPIRY = date(2026, 11, 17)
OVERRIDE = '/etc/systemd/system/certbot-renew.timer.d/override.conf'
HOOK_SCRIPT = '/usr/local/sbin/foundry-cert-deploy'
HOOK_DIR = '/etc/letsencrypt/renewal-hooks/deploy'
HOOK_LINK = f'{HOOK_DIR}/foundry.sh'
LINEAGE = f'/etc/letsencrypt/live/{HOSTNAME}/fullchain.pem'
SERVED_CERT = '/foundry/foundrydata/Config/foundry.pem'
# Host control runs this script dispatched, for the message when it stops early.
DISPATCHED = []


class Failure(Exception):
    pass


class Record:
    """What the run found, as the issue's checklist."""

    def __init__(self):
        self.lines = []
        self.notes = []

    def check(self, passed, text):
        mark = 'x' if passed else ' '
        self.lines.append(f'- [{mark}] {text}')
        print(('PASS  ' if passed else 'FAIL  ') + text)

    def note(self, text):
        self.notes.append(text)
        print('      ' + text)

    def markdown(self, started, instance_id, outcome):
        when = started.strftime('%Y-%m-%d %H:%M UTC')
        out = [f'Live checks run by Craig on {when} with `scripts/live-checks.py` '
               f'against `{instance_id or "the host"}`. {outcome}', '']
        out.extend(self.lines)
        if self.notes:
            out.append('')
            out.extend(f'- {n}' for n in self.notes)
        return '\n'.join(out) + '\n'


def say(text):
    print(f'\n=== {text}')


def show_command(args):
    print('$ ' + ' '.join(a if ' ' not in a else repr(a) for a in args))


def run(args, timeout=120, check=True, stdin=None):
    """Run a command, print it first, and return its CompletedProcess."""
    show_command(args)
    try:
        result = subprocess.run(args, capture_output=True, text=True, timeout=timeout,
                                input=stdin)
    except FileNotFoundError:
        raise Failure(f'{args[0]} is not installed or not on PATH')
    except subprocess.TimeoutExpired:
        raise Failure(f'{args[0]} did not finish within {timeout} seconds')
    if check and result.returncode:
        raise Failure(f'{args[0]} failed ({result.returncode}): '
                      f'{(result.stderr or result.stdout).strip()}')
    return result


def aws(profile, *args, timeout=120, check=True):
    """Run the AWS CLI with a profile. With check, return parsed JSON."""
    result = run(['aws', '--profile', profile, '--region', REGION, '--output', 'json', *args],
                 timeout=timeout, check=check)
    if check:
        return json.loads(result.stdout) if result.stdout.strip() else None
    return result


def gh_api(path, method='GET', body=None, timeout=60):
    args = ['gh', 'api', '-X', method, path]
    stdin = None
    if body is not None:
        args += ['--input', '-']
        stdin = json.dumps(body)
        print('  body: ' + stdin)
    result = run(args, timeout=timeout, stdin=stdin)
    return json.loads(result.stdout) if result.stdout.strip() else None


def gh_api_retry(path, tries=5):
    """A GET that rides out a few transient gh or GitHub errors."""
    for attempt in range(tries):
        try:
            return gh_api(path)
        except Failure as exc:
            if attempt == tries - 1:
                raise
            print(f'  retrying after: {exc}')
            time.sleep(15)


def ask(prompt):
    try:
        return input(prompt).strip().lower()
    except EOFError:
        raise Failure(f'No answer (end of input) at: {prompt}')


def ask_one_of(prompt, answers):
    while True:
        answer = ask(f'{prompt} [{"/".join(answers)}] ')
        if answer in answers:
            return answer
        print(f'Please answer one of: {", ".join(answers)}')


# --- Preflight ---

def ensure_credentials(profile):
    """The profile answers get-caller-identity in the workloads account, after a login if needed."""
    identity = aws(profile, 'sts', 'get-caller-identity', check=False)
    if identity.returncode:
        print(identity.stderr.strip())
        print(f'No credentials for {profile}; signing in to Identity Center (this opens a browser).')
        login = ['aws', 'sso', 'login', '--profile', profile]
        show_command(login)
        if subprocess.run(login).returncode:
            raise Failure(f'aws sso login for {profile} did not complete')
        identity = aws(profile, 'sts', 'get-caller-identity', check=False)
        if identity.returncode:
            raise Failure(f'{profile} still has no credentials: {identity.stderr.strip()}')
    who = json.loads(identity.stdout)
    if who['Account'] != ACCOUNT:
        raise Failure(f'{profile} is in account {who["Account"]}, not {ACCOUNT}; check ~/.aws/config')
    print(f'{profile}: {who["Arn"]}')


def preflight():
    say('Preflight')
    if not sys.stdin.isatty():
        raise Failure('Run this at a terminal: it asks questions along the way')
    for tool in ('gh', 'aws', 'openssl', 'curl'):
        if shutil.which(tool) is None:
            raise Failure(f'{tool} is not installed or not on PATH')
    run(['gh', 'auth', 'status'])
    for profile in (READ_PROFILE, ADMIN_PROFILE):
        ensure_credentials(profile)


# --- The host ---

def find_host():
    """The one instance tagged Name=foundry and project=foundry, as the Taskfile finds it."""
    reservations = aws(READ_PROFILE, 'ec2', 'describe-instances',
                       '--filters', 'Name=tag:Name,Values=foundry',
                       'Name=tag:project,Values=foundry',
                       'Name=instance-state-name,Values=pending,running,stopping,stopped')
    instances = [i for r in reservations['Reservations'] for i in r['Instances']]
    if len(instances) != 1:
        ids = ', '.join(i['InstanceId'] for i in instances) or 'none'
        raise Failure(f'Expected exactly one host tagged Name=foundry and project=foundry, found: {ids}')
    instance_id = instances[0]['InstanceId']
    if instance_id != EXPECTED_INSTANCE:
        print(f'Note: the host is {instance_id}, not {EXPECTED_INSTANCE} as in docs/operations.md '
              '(a rebuild changes the ID).')
    return instance_id, instances[0]['State']['Name']


def host_state(instance_id):
    data = aws(READ_PROFILE, 'ec2', 'describe-instances', '--instance-ids', instance_id)
    return data['Reservations'][0]['Instances'][0]['State']['Name']


def host_state_or_unknown(instance_id):
    try:
        return host_state(instance_id)
    except Failure as exc:
        return f'unknown ({exc})'


def wait_for_state(instance_id, wanted, minutes):
    deadline = time.monotonic() + minutes * 60
    while True:
        state = host_state(instance_id)
        print(f'{instance_id} is {state}')
        if state == wanted:
            return state
        if time.monotonic() > deadline:
            raise Failure(f'{instance_id} is {state}, not {wanted}, after {minutes} minutes')
        time.sleep(15)


# --- Host control, the GitHub Actions workflow ---

def newest_run_id():
    runs = gh_api_retry(f'repos/{REPO}/actions/workflows/{WORKFLOW}/runs?per_page=1')
    return max((r['id'] for r in runs['workflow_runs']), default=0)


def host_control(action, instance_id, nobody_playing=False):
    """Dispatch the Host control workflow on main and wait for its run."""
    say(f'Host control: {action}')
    watermark = newest_run_id()  # run IDs only grow, so ours is the first one above this
    gh_api(f'repos/{REPO}/actions/workflows/{WORKFLOW}/dispatches', method='POST', body={
        'ref': 'main',
        'inputs': {'action': action, 'nobody_playing': 'true' if nobody_playing else 'false'},
    })
    DISPATCHED.append(f'{action} (run not found yet)')
    run_info = None
    for _ in range(36):
        time.sleep(5)
        runs = gh_api_retry(f'repos/{REPO}/actions/workflows/{WORKFLOW}/runs'
                            '?event=workflow_dispatch&per_page=5')
        newer = [r for r in runs['workflow_runs'] if r['id'] > watermark]
        if newer:
            run_info = min(newer, key=lambda r: r['id'])
            break
    if not run_info:
        raise Failure('The workflow was dispatched but no run appeared within three minutes. '
                      f'The host is {host_state_or_unknown(instance_id)}; watch '
                      f'https://github.com/{REPO}/actions/workflows/{WORKFLOW}')
    print(f'Run {run_info["id"]}: {run_info["html_url"]}')
    DISPATCHED[-1] = f'{action} ({run_info["html_url"]})'
    deadline = time.monotonic() + 60 * 60
    last = None
    while run_info['status'] != 'completed':
        if time.monotonic() > deadline:
            raise Failure(f'Run {run_info["id"]} is still {run_info["status"]} after an hour; '
                          f'the host is {host_state_or_unknown(instance_id)}')
        time.sleep(20)
        run_info = gh_api_retry(f'repos/{REPO}/actions/runs/{run_info["id"]}')
        if run_info['status'] != last:
            last = run_info['status']
            waiting = '' if last == 'in_progress' else ' (waiting for a runner or for another host run)'
            print(f'  {last}{waiting}')
    if run_info['conclusion'] != 'success':
        raise Failure(f'Host control {action} ended with {run_info["conclusion"]}: '
                      f'{run_info["html_url"]}. The host is {host_state_or_unknown(instance_id)}')
    print(f'Host control {action} succeeded.')
    return run_info['html_url']


# --- Run Command, with workloads-admin ---

def run_on_host(instance_id, comment, script, timeout=900):
    """Run a shell script on the host as root through SSM, and return its output."""
    say(f'On the host: {comment}')
    print(script.rstrip())
    parameters = json.dumps({'commands': script.splitlines(), 'executionTimeout': [str(timeout)]})
    command_id = None
    for attempt in range(24):
        result = aws(ADMIN_PROFILE, 'ssm', 'send-command', '--instance-ids', instance_id,
                     '--document-name', 'AWS-RunShellScript', '--comment', comment,
                     '--parameters', parameters, check=False)
        if result.returncode == 0:
            command_id = json.loads(result.stdout)['Command']['CommandId']
            break
        # A host that has just booted may not have registered with SSM yet.
        if 'InvalidInstanceId' not in result.stderr:
            raise Failure(f'send-command failed: {result.stderr.strip()}')
        print('The host has not registered with SSM yet; waiting.')
        time.sleep(5)
    if command_id is None:
        raise Failure('The host did not register with SSM within two minutes')
    print(f'Command {command_id}')
    status = 'Pending'
    invocation = None
    deadline = time.monotonic() + timeout + 120
    while status in ('Pending', 'InProgress', 'Delayed'):
        if time.monotonic() > deadline:
            raise Failure(f'Command {command_id} is still {status}')
        time.sleep(5)
        result = aws(ADMIN_PROFILE, 'ssm', 'get-command-invocation', '--instance-id', instance_id,
                     '--command-id', command_id, check=False)
        if result.returncode:
            if 'InvocationDoesNotExist' in result.stderr:
                continue  # the invocation takes a moment to appear after sending
            raise Failure(f'get-command-invocation failed: {result.stderr.strip()}')
        invocation = json.loads(result.stdout)
        status = invocation['Status']
    stdout = invocation.get('StandardOutputContent', '')
    stderr = invocation.get('StandardErrorContent', '')
    print('--- output')
    print(stdout.rstrip())
    if stderr.strip():
        print('--- stderr')
        print(stderr.rstrip())
    print(f'--- status {status}')
    if status != 'Success':
        raise Failure(f'Command {command_id} ({comment}) ended with {status}; the host is still running')
    return stdout


# A read. Its exit status is always 0: the Python decides what passed.
CHECK_SCRIPT = f'''\
echo "== renew in progress"; n=0; while s=$(systemctl is-active certbot-renew.service); [ "$s" = activating ] || [ "$s" = active ] || [ "$s" = deactivating ]; do n=$((n+1)); [ $n -lt 120 ] || break; sleep 5; done; echo "waited $n"
echo "== findmnt /foundry"; findmnt -n -o SOURCE,FSTYPE,OPTIONS /foundry || echo "NOT MOUNTED"
echo "== foundry.service"; for i in $(seq 1 12); do s=$(systemctl is-active foundry.service); [ "$s" = activating ] || break; sleep 5; done; echo "$s"
echo "== certbot-renew.timer enabled"; systemctl is-enabled certbot-renew.timer || true
echo "== certbot-renew.timer persistent"; systemctl show certbot-renew.timer -p Persistent
echo "== next elapse"; systemctl show certbot-renew.timer -p NextElapseUSecRealtime --value
echo "== last result"; systemctl show certbot-renew.service -p Result --value
echo "== unit files"; systemctl cat certbot-renew.timer certbot-renew.service --no-pager
echo "== next run"; systemctl list-timers certbot-renew.timer --no-pager --all
echo "== last runs"; journalctl -u certbot-renew.service -n 20 --no-pager
echo "== hook script"; if [ -x {HOOK_SCRIPT} ]; then echo executable; else echo "missing or not executable"; fi
echo "== hook target"; if [ -L {HOOK_LINK} ]; then readlink -f {HOOK_LINK}; elif [ -e {HOOK_LINK} ]; then echo "a plain file, not a link"; else echo missing; fi
echo "== hook entries"; ls -A1 {HOOK_DIR} 2>&1
echo "== certificates"; certbot certificates 2>&1
echo "== lineage"; openssl x509 -noout -enddate -fingerprint -sha256 -in {LINEAGE} 2>&1
echo "== served file"; openssl x509 -noout -enddate -fingerprint -sha256 -in {SERVED_CERT} 2>&1
exit 0
'''

FIX_TIMER_SCRIPT = f'''\
set -e
f={OVERRIDE}
test -f "$f" || {{ echo "$f is missing; the host was not built by user data as expected" >&2; exit 1; }}
grep -q '^Persistent=' "$f" || sed -i '/^\\[Timer\\]/a Persistent=true' "$f"
systemctl daemon-reload
systemctl restart certbot-renew.timer
echo "== override"; cat "$f"
echo "== persistent"; systemctl show certbot-renew.timer -p Persistent
echo "== next elapse"; systemctl show certbot-renew.timer -p NextElapseUSecRealtime --value
echo "== next run"; systemctl list-timers certbot-renew.timer --no-pager --all
'''

FIX_HOOK_SCRIPT = f'''\
set -e
test -x {HOOK_SCRIPT} || {{ echo "{HOOK_SCRIPT} is missing; user data did not install it" >&2; exit 1; }}
install -d -m 0755 {HOOK_DIR}
ln -sfn {HOOK_SCRIPT} {HOOK_LINK}
echo "== hook target"; readlink -f {HOOK_LINK}
echo "== hook entries"; ls -A1 {HOOK_DIR}
'''

RENEW_SCRIPT = f'''\
systemctl start certbot-renew.service; rc=$?; echo "== renew exit"; echo "$rc"
echo "== journal"; journalctl -u certbot-renew.service -n 30 --no-pager
echo "== certificates"; certbot certificates 2>&1
echo "== lineage"; openssl x509 -noout -enddate -fingerprint -sha256 -in {LINEAGE} 2>&1
echo "== served file"; openssl x509 -noout -enddate -fingerprint -sha256 -in {SERVED_CERT} 2>&1
echo "== foundry.service"; for i in $(seq 1 24); do s=$(systemctl is-active foundry.service); [ "$s" = activating ] || break; sleep 5; done; echo "$s"
exit 0
'''


def section(output, name):
    """The lines of a script's output under its `== name` heading."""
    match = re.search(rf'^== {re.escape(name)}\n(.*?)(?=^== |\Z)', output, re.S | re.M)
    return match.group(1).strip() if match else ''


def not_after(text):
    """The date in openssl's `notAfter=Nov 17 03:14:15 2026 GMT` line, or None."""
    match = re.search(r'notAfter=(.+)', text)
    if not match:
        return None
    try:
        return datetime.strptime(match.group(1).strip(), '%b %d %H:%M:%S %Y %Z').replace(
            tzinfo=timezone.utc)
    except ValueError:
        return None


def day(when):
    return f'{when:%Y-%m-%d}' if when else 'unreadable'


def fingerprint(text):
    """The value of openssl's `sha256 Fingerprint=AB:CD:...` line, or None."""
    match = re.search(r'Fingerprint=([0-9A-Fa-f:]+)', text)
    return match.group(1).upper() if match else None


def package_sets_persistent(unit_files):
    """Whether a unit file under /usr/lib (the certbot package's) sets Persistent=true."""
    current = ''
    for line in unit_files.splitlines():
        if line.startswith('# /'):
            current = line[2:].strip()
        elif line.strip() == 'Persistent=true' and current.startswith('/usr/lib/'):
            return True
    return False


# --- From this computer ---

def join_answers(minutes):
    """Wait until /join answers 200 over a verified TLS connection."""
    say('From this computer: /join')
    deadline = time.monotonic() + minutes * 60
    while True:
        result = run(['curl', '-sS', '-o', '/dev/null', '-w', '%{http_code}', '--max-time', '15',
                      f'{BASE_URL}/join'], check=False, timeout=30)
        code = result.stdout.strip()
        if code == '200':
            return True
        print(f'  {code or result.stderr.strip()}')
        if time.monotonic() > deadline:
            return False
        time.sleep(10)


def served_expiry():
    say('From this computer: the served certificate')
    client = run(['openssl', 's_client', '-connect', f'{HOSTNAME}:{PORT}',
                  '-servername', HOSTNAME], stdin='', check=False, timeout=30)
    cert = run(['openssl', 'x509', '-noout', '-enddate', '-subject'],
               stdin=client.stdout, check=False)
    print(cert.stdout.strip() or cert.stderr.strip())
    return not_after(cert.stdout)


def foundry_status():
    """Foundry's own /api/status: the active world and user count."""
    result = run(['curl', '-sS', '--max-time', '15', f'{BASE_URL}/api/status'], check=False)
    try:
        data = json.loads(result.stdout)
    except ValueError:
        return None
    return data if isinstance(data, dict) else None


# --- The run ---

def checks(args, record, instance_id, initial_state):
    """Every step after the host is known. Raises Failure to stop early."""
    record.note(f'The host was `{initial_state}` when the run began.')
    already_up = initial_state in ('running', 'pending')
    if already_up:
        answer = ask_one_of(f'The host is already {initial_state}. Is anyone playing or preparing?',
                            ['y', 'n'])
        if answer == 'y':
            raise Failure('Nothing to do during a session: run this again after it ends')

    # 1. Start session (part 2 of the issue: EC2 reaches running, /join answers)
    start_url = host_control('start', instance_id)
    state = host_state(instance_id)
    if already_up:
        record.note(f'Host control start only confirmed that Foundry answers, since the host was '
                    f'already {initial_state} ({start_url}); the stopped-to-running step was not exercised.')
    else:
        record.check(state == 'running',
                     f'EC2 reaches `running` after Host control start from `{initial_state}` ({start_url})')
    record.check(join_answers(5), '`/join` answers 200 after the start')

    # 2. Read the host (part 1: the renewal check of aws-design section 12, plus the mount and service)
    output = run_on_host(instance_id, 'Live checks: certificate renewal and host state', CHECK_SCRIPT)
    waited = section(output, 'renew in progress')
    if waited and waited != 'waited 0':
        record.note('A certbot-renew.service run was in progress when the check began '
                    f'({waited.replace("waited ", "")} x 5 s), presumably the timer catching up after the boot.')
    record.check('NOT MOUNTED' not in section(output, 'findmnt /foundry'), '`/foundry` is mounted')
    service = section(output, 'foundry.service')
    record.check(service == 'active', f'`foundry.service` is active (found `{service}`)')
    record.check(section(output, 'certbot-renew.timer enabled') == 'enabled',
                 '`certbot-renew.timer` is enabled')
    persistent = section(output, 'certbot-renew.timer persistent')
    from_package = package_sets_persistent(section(output, 'unit files'))
    record.note(f'Before any fix: `{persistent}`; the certbot package\'s own timer unit '
                f'{"sets" if from_package else "does not set"} `Persistent=true`.')
    script_ok = section(output, 'hook script') == 'executable'
    hook_target = section(output, 'hook target')
    hook_ok = script_ok and hook_target == HOOK_SCRIPT
    hook_entries = [e for e in section(output, 'hook entries').splitlines() if e]
    before_lineage = not_after(section(output, 'lineage'))
    before_served = not_after(section(output, 'served file'))
    certbot_says = re.search(r'Expiry Date: (\S+)', section(output, 'certificates'))
    record.note(f'Before the run: certbot\'s lineage expires {day(before_lineage)}'
                f'{" (certbot: " + certbot_says.group(1) + ")" if certbot_says else ""}, '
                f'`Config/foundry.pem` expires {day(before_served)}.')
    last_result = section(output, 'last result')
    record.check(last_result in ('success', ''),
                 f'The last certbot-renew.service run had no errors (Result=`{last_result or "never ran"}`)')

    # 3. Fix what the check found
    if persistent != 'Persistent=yes':
        fixed = run_on_host(instance_id, 'Make certbot-renew.timer persistent', FIX_TIMER_SCRIPT)
        record.check(section(fixed, 'persistent') == 'Persistent=yes',
                     'Added `Persistent=true` to the timer override, reloaded and restarted the timer')
        next_elapse = section(fixed, 'next elapse')
    else:
        record.check(True, '`certbot-renew.timer` is persistent (no change needed)')
        next_elapse = section(output, 'next elapse')
    record.check(next_elapse not in ('', 'n/a'), f'The timer has a next run (`{next_elapse or "none"}`)')
    if not script_ok:
        record.check(False, f'`{HOOK_SCRIPT}` is missing or not executable: user data installs it, '
                            'so the host was not built as expected; nothing relinked')
    elif not hook_ok:
        relinked = run_on_host(instance_id, 'Relink the certificate deploy hook', FIX_HOOK_SCRIPT)
        record.check(section(relinked, 'hook target') == HOOK_SCRIPT,
                     f'Relinked `{HOOK_LINK}` to `{HOOK_SCRIPT}` (it was {hook_target or "missing"})')
        hook_entries = [e for e in section(relinked, 'hook entries').splitlines() if e]
    else:
        record.check(True, f'`{HOOK_LINK}` links to `{HOOK_SCRIPT}`, which is executable')
    extras = [e for e in hook_entries if e != 'foundry.sh' and 'No such file' not in e]
    record.check(not extras, '`foundry.sh` is the only entry in the deploy hook directory'
                 + (f' (also found: {", ".join(extras)}; remove them by hand)' if extras else ''))

    # 4. Renew now
    if args.no_renew:
        record.note('Renewal left to the timer (--no-renew).')
    else:
        renewed = run_on_host(instance_id, 'Renew the certificate now', RENEW_SCRIPT, timeout=1200)
        exit_code = section(renewed, 'renew exit')
        lineage = not_after(section(renewed, 'lineage'))
        served_file = not_after(section(renewed, 'served file'))
        renewed_now = lineage is not None and before_lineage is not None and lineage > before_lineage
        if renewed_now:
            record.check(True, f'Certbot renewed the certificate in this run (lineage expires {day(lineage)})')
        elif lineage is not None and lineage.date() > CURRENT_EXPIRY:
            record.check(True, f'The certificate was already renewed before this run (lineage expires '
                               f'{day(lineage)}); certbot-renew.service exited {exit_code or "?"}')
        else:
            record.check(False, f'Certbot did not renew the certificate (lineage expires {day(lineage)}, '
                                f'certbot-renew.service exited {exit_code or "?"}): read the journal above')
        same = fingerprint(section(renewed, 'lineage'))
        record.check(same is not None and same == fingerprint(section(renewed, 'served file')),
                     f'`Config/foundry.pem` holds the lineage\'s certificate (same SHA-256 fingerprint, '
                     f'expires {day(served_file)})')
        record.check(section(renewed, 'foundry.service') == 'active',
                     '`foundry.service` is active after the deploy hook restarted it' if renewed_now
                     else '`foundry.service` is active (no renewal was due, so the hook did not run)')
        record.check(join_answers(5), '`/join` answers 200 after the renewal')

    # 5. What the world sees
    expiry = served_expiry()
    if expiry is None:
        record.check(False, 'Could not read the served certificate from this computer')
    else:
        record.check(expiry.date() > CURRENT_EXPIRY,
                     f'The served certificate expires {day(expiry)}, later than {CURRENT_EXPIRY}')
    status = foundry_status()
    if status:
        record.note(f'Foundry `/api/status`: world `{status.get("world")}`, '
                    f'version {status.get("version")}, users {status.get("users")}.')

    # 6. The browser checks only a person can do
    if not args.no_browser_pause:
        say('In your browser')
        print(f'Open {BASE_URL} and check:')
        print('  - both worlds open (Return to Setup, launch the other world, then back);')
        print('  - the GM login works;')
        print('  - a player can join and a change saves (part 3 of the issue), if you have one handy.')
        for text in ('Both worlds open', 'GM login', 'A player joined and a change saved (part 3)'):
            answer = ask_one_of(f'{text}:', ['pass', 'fail', 'skip'])
            if answer == 'skip':
                record.note(f'{text}: not checked in this run.')
            else:
                record.check(answer == 'pass', f'{text} (checked in the browser)')

    # 7. End session (part 2: the host powers off and EC2 reaches stopped)
    if args.keep_running:
        record.note('The host was left running (--keep-running).')
    else:
        answer = ask_one_of('End the session now with Host control? "stop" means nobody is playing.',
                            ['stop', 'keep'])
        if answer == 'stop':
            ensure_credentials(READ_PROFILE)  # the pause may have outlasted the sign-in
            stop_url = host_control('stop', instance_id, nobody_playing=True)
            state = wait_for_state(instance_id, 'stopped', 10)
            record.check(state == 'stopped', f'EC2 reaches `stopped` after Host control stop ({stop_url})')
        else:
            record.note('The host was left running at your request; stop it with Host control.')


def write_record(record, started, instance_id, outcome):
    say('Record')
    text = record.markdown(started, instance_id, outcome)
    path = os.path.abspath(f'live-checks-{started:%Y%m%dT%H%M%SZ}.md')
    with open(path, 'w') as out:
        out.write(text)
    print(text)
    print(f'Written to {path}')
    return path


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument('--no-renew', action='store_true',
                        help='do not run certbot-renew.service; leave the renewal to the timer')
    parser.add_argument('--keep-running', action='store_true',
                        help='leave the host running at the end instead of asking')
    parser.add_argument('--no-browser-pause', action='store_true',
                        help='do not wait for the browser checks before stopping')
    args = parser.parse_args()

    started = datetime.now(timezone.utc)
    record = Record()
    preflight()
    say('The host')
    instance_id, initial_state = find_host()
    print(f'{instance_id} is {initial_state}')
    try:
        checks(args, record, instance_id, initial_state)
    except (Exception, KeyboardInterrupt) as exc:
        state = host_state_or_unknown(instance_id)
        if isinstance(exc, KeyboardInterrupt):
            reason = 'interrupted'
        elif isinstance(exc, Failure):
            reason = str(exc)
        else:
            reason = f'unexpected error: {exc!r}'
        # The record may be posted publicly: keep the first line only, without role ARNs.
        public = re.sub(r'arn:aws[^\s]*', 'arn:...', reason.splitlines()[0] if reason else reason)
        record.note(f'The run stopped early: {public}. The host was `{state}` at that point.')
        if DISPATCHED:
            record.note('Host control runs dispatched by this run: ' + '; '.join(DISPATCHED) + '.')
        write_record(record, started, instance_id, 'The run stopped early; see the last note.')
        print(f'\nStopped: {reason}', file=sys.stderr)
        print(f'The host is {state}.', file=sys.stderr)
        if state != 'stopped' or any(d.startswith('start') for d in DISPATCHED):
            print('A Host control start may still be running or the host may be up. When the session '
                  'is over, end it with Host control (stop, nobody playing): '
                  f'https://github.com/{REPO}/actions/workflows/{WORKFLOW}', file=sys.stderr)
        if not isinstance(exc, (Failure, KeyboardInterrupt)):
            raise
        return 130 if isinstance(exc, KeyboardInterrupt) else 1
    path = write_record(record, started, instance_id, 'Every step ran.')
    try:
        if ask_one_of(f'Post this as a comment on {REPO}#{ISSUE}?', ['y', 'n']) == 'y':
            run(['gh', 'issue', 'comment', str(ISSUE), '--repo', REPO, '--body-file', path])
            print('Posted.')
    except (Failure, KeyboardInterrupt) as exc:
        print(f'\nNot posted ({exc if isinstance(exc, Failure) else "interrupted"}); the record is at '
              f'{path}. The host is {host_state_or_unknown(instance_id)}.', file=sys.stderr)
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except Failure as exc:
        # Before the host is known: nothing has been started.
        print(f'\nStopped: {exc}', file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        # Before the host is known: nothing has been started.
        print('\nInterrupted before anything ran on the host.', file=sys.stderr)
        sys.exit(130)
