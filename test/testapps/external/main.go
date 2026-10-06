package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"gostalgia/sdk"
)

type externalApp struct {
	state string
}

func (a *externalApp) Init(ctx *sdk.Context) error {
	a.state = "initialized"
	_ = ctx.Handle("greet", func(c context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Name string `json:"name"`
		}
		_ = sdk.DecodeParams(raw, &p)
		return map[string]string{
			"greeting": "hello " + p.Name,
			"state":    a.state,
		}, nil
	})
	_ = ctx.Handle("crash", func(c context.Context, raw json.RawMessage) (any, error) {
		os.Exit(42)
		return nil, nil
	})
	_ = ctx.Handle("hang", func(c context.Context, raw json.RawMessage) (any, error) {
		select {}
	})
	// presentation routes
	viewFn := func(c context.Context) (sdk.View, error) {
		return sdk.View{
			State:  sdk.ViewReady,
			Title:  "External Test App",
			Status: a.state,
			Fields: []sdk.Field{
				{ID: "state", Label: "State"},
			},
			Actions: []sdk.Action{
				{ID: "set_state", Label: "Set State"},
			},
		}, nil
	}
	actionFn := func(c context.Context, r sdk.ActionRequest) (sdk.View, error) {
		if r.Action == "set_state" {
			a.state = r.Values["state"]
		}
		return viewFn(c)
	}
	return ctx.Present(viewFn, actionFn)
}

func (a *externalApp) Run(ctx context.Context) error {
	fmt.Fprintln(os.Stdout, "external app started")
	<-ctx.Done()
	fmt.Fprintln(os.Stdout, "external app shutting down")
	return ctx.Err()
}

func (a *externalApp) Stop(ctx context.Context) error {
	fmt.Fprintln(os.Stdout, "external app stopped")
	return nil
}

func main() {
	if err := sdk.Serve(&externalApp{}); err != nil {
		fmt.Fprintf(os.Stderr, "serve error: %v\n", err)
		os.Exit(1)
	}
}
