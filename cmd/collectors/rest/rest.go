package rest

import (
	"fmt"
	"github.com/tidwall/gjson"
	"goharvest2/cmd/poller/collector"
	"goharvest2/cmd/poller/plugin"
	"goharvest2/pkg/api/ontapi/rest"
	"goharvest2/pkg/errors"
	"goharvest2/pkg/matrix"
	"strconv"
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

	if r.client, err = r.getClient(); err != nil {
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

func (r *Rest) getClient() (*rest.Client, error) {
	var (
		addr, x             string
		useInsecureTls      bool
		certAuth, basicAuth [2]string
	)

	if addr = r.Params.GetChildContentS("addr"); addr == "" {
		return nil, errors.New(errors.MISSING_PARAM, "addr")
	}

	if x = r.Params.GetChildContentS("use_insecure_tls"); x != "" {
		useInsecureTls, _ = strconv.ParseBool(x)
	}

	// set authentication method
	if r.Params.GetChildContentS("auth_style") == "certificate_auth" {

		certAuth[0] = r.Params.GetChildContentS("ssl_cert")
		certAuth[1] = r.Params.GetChildContentS("ssl_key")

		return rest.New(addr, &certAuth, nil, useInsecureTls)
	}

	basicAuth[0] = r.Params.GetChildContentS("username")
	basicAuth[1] = r.Params.GetChildContentS("password")

	return rest.New(addr, nil, &basicAuth, useInsecureTls)
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
