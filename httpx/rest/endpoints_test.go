package rest_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
)

func TestEndpointsDescribeRegisteredOperations(t *testing.T) {
	api := rest.New(rest.Options{Mode: rest.ModeOpenAPI})
	get := huma.Operation{OperationID: "count-get", Method: http.MethodGet, Path: "/counts/{id}", Summary: "取计数"}
	rest.RequireBearer(api, func(context.Context, string) (identity.Subject, error) { return identity.Subject{}, nil })(&get)
	rest.Register[countIn, minimalOutput](api, get, nil)
	stream := huma.Operation{OperationID: "count-stream", Method: http.MethodGet, Path: "/count"}
	rest.Public()(&stream)
	rest.RegisterSSE[countIn](api, stream, map[string]any{"tick": &tick{}}, nil)

	eps := api.Endpoints()
	if len(eps) != 2 {
		t.Fatalf("%+v", eps)
	}
	if e := eps[0]; e.OperationID != "count-get" || e.Path != "/counts/{id}" || e.Summary != "取计数" || e.Public ||
		e.Input != reflect.TypeFor[countIn]() || e.Output != reflect.TypeFor[minimalOutput]() || e.Events != nil {
		t.Fatalf("%+v", e)
	}
	if e := eps[1]; !e.Public || e.Output != reflect.TypeFor[huma.StreamResponse]() ||
		!reflect.DeepEqual(e.Events, map[string]reflect.Type{"tick": reflect.TypeFor[tick]()}) {
		t.Fatalf("%+v", e)
	}
}
