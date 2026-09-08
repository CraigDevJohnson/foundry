package main

import (
	"context"
	"encoding/json"
	"io"
	"os"

	"github.com/CraigDevJohnson/foundry/internal/app"
	"github.com/CraigDevJohnson/foundry/internal/status"
)

func main() {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, status.MaxRequestBytes+1))
	var response status.Response
	if err != nil {
		response = status.ErrorResponse("", "invalid_request")
	} else {
		response, _ = app.Handler().Handle(context.Background(), raw)
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		os.Exit(1)
	}
	if response.Kind == "error" {
		os.Exit(1)
	}
}
