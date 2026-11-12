package realtime

import (
	"context"

	"github.com/cockroachdb/errors"
	"go.opentelemetry.io/otel/metric"
)

// scopeName 是实时频道指标的 instrumentation scope。
const scopeName = "github.com/ryan053857/mygoravel/realtime"

// observe 登记两个指标：realtime.connections 是本实例当前的连接数，realtime.subscriptions 是这些连接的订阅数。
func (rt *Realtime) observe(mp metric.MeterProvider) (metric.Registration, error) {
	meter := mp.Meter(scopeName)
	connections, err := meter.Int64ObservableGauge("realtime.connections", metric.WithUnit("{connection}"),
		metric.WithDescription("Client connections on this instance."))
	if err != nil {
		return nil, errors.Wrap(err, "realtime: create realtime.connections")
	}
	subscriptions, err := meter.Int64ObservableGauge("realtime.subscriptions", metric.WithUnit("{subscription}"),
		metric.WithDescription("Channel subscriptions held by connections on this instance."))
	if err != nil {
		return nil, errors.Wrap(err, "realtime: create realtime.subscriptions")
	}
	reg, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		hub := rt.node.Hub()
		o.ObserveInt64(connections, int64(hub.NumClients()))
		o.ObserveInt64(subscriptions, int64(hub.NumSubscriptions()))
		return nil
	}, connections, subscriptions)
	return reg, errors.Wrap(err, "realtime: register metrics")
}
