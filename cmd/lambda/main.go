package main

import (
	"github.com/CraigDevJohnson/foundry/internal/app"
	"github.com/aws/aws-lambda-go/lambda"
)

func main() { lambda.StartHandler(app.Handler()) }
