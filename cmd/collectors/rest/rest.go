package rest

import (
	"fmt"
	"github.com/tidwall/gjson"
	"goharvest2/cmd/poller/collector"
	"goharvest2/cmd/poller/options"
	"goharvest2/cmd/poller/plugin"
	"goharvest2/pkg/api/ontapi/rest"
	"goharvest2/pkg/conf"
	"goharvest2/pkg/errors"
	"goharvest2/pkg/matrix"
	"goharvest2/pkg/util"
	"time"
)

type Rest struct {
	*collector.AbstractCollector
	client         *rest.Client
	apiPath        string
	instanceKeys   []string
	instanceLabels map[string]string
}

func init() {
	plugin.RegisterModule(Rest{})
}

func (Rest) HarvestModule() plugin.ModuleInfo {
	return plugin.ModuleInfo{
		ID:  "harvest.collector.rest",
		New: func() plugin.Module { return new(Rest) },
	}
}

func (r *Rest) Init(a *collector.AbstractCollector) error {

	var err error

	r.AbstractCollector = a
	if err = collector.Init(r); err != nil {
		return err
	}

	a.GetOptions()
	if r.client, err = r.getClient(a.GetOptions()); err != nil {
		return err
	}

	if err = r.client.Init(5); err != nil {
		return err
	}

	r.Logger.Info().Msgf("connected to %s: %s", r.client.ClusterName(), r.client.Info())

	r.Matrix.SetGlobalLabel("cluster", r.client.ClusterName())

	if err = r.initCache(r.getTemplateFn(), r.client.Version()); err != nil {
		return err
	}
	r.Logger.Info().Msgf("initialized cache with %d metrics", len(r.Matrix.GetMetrics()))
	return nil
}

func (r *Rest) getClient(opt *options.Options) (*rest.Client, error) {
	var (
		poller              *conf.Poller
		addr                string
		useInsecureTLS      bool
		certAuth, basicAuth [2]string
		err                 error
	)

	if poller, err = conf.GetPoller2(opt.Config, opt.Poller); err != nil {
		r.Logger.Error().Stack().Err(err).Str("poller", opt.Poller).Msgf("")
		return nil, err
	}
	if addr = util.Value(poller.Addr, ""); addr == "" {
		r.Logger.Error().Stack().Str("poller", opt.Poller).Str("addr", addr).Msgf("Invalid address")
		return nil, errors.New(errors.MISSING_PARAM, "addr")
	}

	// by default, enforce secure TLS, if not requested otherwise by user
	if x := poller.UseInsecureTls; x != nil {
		useInsecureTLS = *poller.UseInsecureTls
	} else {
		useInsecureTLS = false
	}

	// set authentication method
	if poller.AuthStyle != nil && *poller.AuthStyle == "certificate_auth" {
		certAuth[0] = util.Value(poller.SslCert, "")
		certAuth[1] = util.Value(poller.SslKey, "")
		if certAuth[0] == "" {
			return nil, errors.New(errors.MISSING_PARAM, "ssl_cert")
		} else if certAuth[1] == "" {
			return nil, errors.New(errors.MISSING_PARAM, "ssl_key")
		}
		return rest.New(addr, &certAuth, nil, useInsecureTLS)
	} else {
		basicAuth[0] = util.Value(poller.Username, "")
		basicAuth[1] = poller.Password
		if basicAuth[0] == "" {
			return nil, errors.New(errors.MISSING_PARAM, "username")
		} else if basicAuth[1] == "" {
			return nil, errors.New(errors.MISSING_PARAM, "password")
		}

		return rest.New(addr, nil, &basicAuth, useInsecureTLS)
	}
}

func (r *Rest) getTemplateFn() string {
	var fn string
	if r.Params.GetChildS("objects") != nil {
		fn = r.Params.GetChildS("objects").GetChildContentS(r.Object)
	}
	return fn
}

func (r *Rest) PollData() (*matrix.Matrix, error) {

	var (
		content      []byte
		count        uint64
		apiD, parseD time.Duration
		startTime    time.Time
		err          error
	)

	r.Logger.Info().Msgf("starting data poll")
	r.Matrix.Reset()

	startTime = time.Now()
	if content, err = r.client.Get(r.apiPath, map[string]string{"fields": "*,"}); err != nil {
		return nil, fmt.Errorf("error calling: %s err=%w", r.apiPath, err)
	}
	apiD = time.Since(startTime)

	startTime = time.Now()
	if !gjson.ValidBytes(content) {
		return nil, fmt.Errorf("json is not valid for: %s", r.apiPath)
	}
	parseD = time.Since(startTime)

	results := gjson.GetManyBytes(content, "num_records", "records")
	numRecords := results[0]
	if numRecords.Int() == 0 {
		return nil, errors.New(errors.ERR_NO_INSTANCE, "no "+r.Object+" instances on cluster")
	}

	r.Logger.Debug().Msgf("extracted %d [%s] instances", numRecords, r.Object)

	results[1].ForEach(func(key, instanceData gjson.Result) bool {
		var (
			instanceKey string
			instance    *matrix.Instance
		)

		if !instanceData.IsObject() {
			r.Logger.Warn().Str("type", instanceData.Type.String()).Msg("skip instance")
			return true
		}

		// extract instance key(s)
		for _, k := range r.instanceKeys {
			value := instanceData.Get(k)
			if value.Exists() {
				instanceKey += value.String()
			} else {
				r.Logger.Warn().Str("key", k).Msg("skip instance, missing key")
				break
			}
		}

		if instanceKey == "" {
			return true
		}

		if instance = r.Matrix.GetInstance(instanceKey); instance == nil {
			if instance, err = r.Matrix.NewInstance(instanceKey); err != nil {
				r.Logger.Error().Msgf("NewInstance [key=%s]: %v", instanceKey, err)
				return true
			}
		}

		for label, display := range r.instanceLabels {
			value := instanceData.Get(label)
			if value.Exists() {
				instance.SetLabel(display, value.String())
				count++
			}
		}

		for key, metric := range r.Matrix.GetMetrics() {

			if metric.GetProperty() == "etl.bool" {
				b := instanceData.Get(key)
				if b.Exists() {
					if err = metric.SetValueBool(instance, b.Bool()); err != nil {
						r.Logger.Error().Err(err).Str("key", key).Msg("SetValueBool metric")
					}
					count++
				}
			} else if metric.GetProperty() == "etl.float" {
				f := instanceData.Get(key)
				if f.Exists() {
					if err = metric.SetValueFloat64(instance, f.Float()); err != nil {
						r.Logger.Error().Err(err).Str("key", key).Msg("SetValueFloat64 metric")
					}
					count++
				}
			}
		}
		return true
	})

	r.Logger.Info().
		Uint64("dataPoints", count).
		Str("apiTime", apiD.String()).
		Str("parseTime", parseD.String()).
		Msg("Collected")

	_ = r.Metadata.LazySetValueInt64("api_time", "data", apiD.Microseconds())
	_ = r.Metadata.LazySetValueInt64("parse_time", "data", parseD.Microseconds())
	_ = r.Metadata.LazySetValueUint64("count", "data", count)
	r.AddCollectCount(count)

	return r.Matrix, nil
}

// Interface guards
var (
	_ collector.Collector = (*Rest)(nil)
)
