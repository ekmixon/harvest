package rest

import (
	"encoding/json"
	"github.com/tidwall/gjson"
	"goharvest2/cmd/poller/collector"
	"goharvest2/cmd/poller/plugin"
	"goharvest2/pkg/api/ontapi/rest"
	"goharvest2/pkg/errors"
	"goharvest2/pkg/matrix"
	//"goharvest2/pkg/tree/node"
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
	r.Logger.Info().Msgf("intialized cache with %d metrics", len(r.Matrix.GetMetrics()))
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
		data         map[string]interface{}
		count        uint64
		apiD, parseD time.Duration
		startTime    time.Time
		err          error
	)

	r.Logger.Info().Msgf("starting data poll")
	r.Matrix.Reset()

	startTime = time.Now()
	if content, err = r.client.Get(r.apiPath, map[string]string{"fields": "*,"}); err != nil {
		return nil, err
	}
	apiD = time.Since(startTime)

	startTime = time.Now()
	if err = json.Unmarshal(content, &data); err != nil {
		return nil, err
	}
	parseD = time.Since(startTime)

	numRecords := gjson.Get(string(content), "num_records")
	if numRecords.Int() == 0 {
		return nil, errors.New(errors.ERR_NO_INSTANCE, "no "+r.Object+" instances on cluster")
	}

	//r.Logger.Debug().Msgf("raw data:\n%s", string(content))
	r.Logger.Debug().Msgf("extracted %d [%s] instances", numRecords, r.Object)

	records := gjson.Get(string(content), "records")

	for _, i := range records.Array() {

		var (
			instanceData string
			instanceKey  string
			instance     *matrix.Instance
		)

		if i.Type != gjson.JSON {
			r.Logger.Warn().Msg("skip instance")
			continue
		}

		instanceData = i.String()

		// extract instance key(s)
		for _, k := range r.instanceKeys {
			value := gjson.Get(instanceData, k)
			if value.Type != gjson.Null {
				instanceKey += value.String()
			} else {
				r.Logger.Warn().Msgf("skip instance, missing key [%s]", k)
				break
			}
		}

		if instanceKey == "" {
			continue
		}

		if instance = r.Matrix.GetInstance(instanceKey); instance == nil {
			if instance, err = r.Matrix.NewInstance(instanceKey); err != nil {
				r.Logger.Error().Msgf("NewInstance [key=%s]: %v", instanceKey, err)
				continue
			}
		}

		for label, display := range r.instanceLabels {
			value := gjson.Get(instanceData, label)
			if value.Type != gjson.Null {
				instance.SetLabel(display, value.String())
				count++
			}
		}

		for key, metric := range r.Matrix.GetMetrics() {

			if metric.GetProperty() == "etl.bool" {
				b := gjson.Get(instanceData, key)
				if b.Type != gjson.Null {
					if err = metric.SetValueBool(instance, b.Bool()); err != nil {
						r.Logger.Error().Msgf("SetValueBool [metric=%s]: %v", key, err)
					}
					count++
				}
			} else if metric.GetProperty() == "etl.float" {
				f := gjson.Get(instanceData, key)
				if f.Type != gjson.Null {
					if err = metric.SetValueFloat64(instance, f.Float()); err != nil {
						r.Logger.Error().Msgf("SetValueFloat64 [metric=%s]: %v", key, err)
					}
					count++
				}
			}
		}
	}

	r.Logger.Info().Msgf("collected %d data points (api time = %s) (parse time = %s)", count, apiD.String(), parseD.String())

	r.Metadata.LazySetValueInt64("api_time", "data", apiD.Microseconds())
	r.Metadata.LazySetValueInt64("parse_time", "data", parseD.Microseconds())
	r.Metadata.LazySetValueUint64("count", "data", count)
	r.AddCollectCount(count)

	return r.Matrix, nil
}
