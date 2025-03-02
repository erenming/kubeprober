// Copyright (c) 2021 Terminus, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/erda-project/kubeprober/pkg/probe-master/k8sclient"

	kubeproberv1 "github.com/erda-project/kubeprober/apis/v1"
	"github.com/erda-project/kubeprober/apistructs"
	"github.com/erda-project/kubeprober/pkg/probe-master/alert/dingding"
	_ "github.com/erda-project/kubeprober/pkg/probe-master/k8sclient"
	httphandler "github.com/erda-project/kubeprober/pkg/probe-master/tunnel-server/handler"
	"github.com/gorilla/mux"
	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	influxdb2api "github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/rancher/remotedialer"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const DINGDING_ALERT_NAME = "dingding"

func clusterRegister(server *remotedialer.Server, rw http.ResponseWriter, req *http.Request) {
	server.ServeHTTP(rw, req)
}

func heartbeat(rw http.ResponseWriter, req *http.Request) {
	hbData := apistructs.HeartBeatReq{}
	cluster := &kubeproberv1.Cluster{}
	var err error

	if err := json.NewDecoder(req.Body).Decode(&hbData); err != nil {
		rw.WriteHeader(http.StatusBadRequest)
		return
	}
	clusterSpec := kubeproberv1.Cluster{
		Spec: kubeproberv1.ClusterSpec{
			K8sVersion: hbData.Version,
			ClusterConfig: kubeproberv1.ClusterConfig{
				Address:         hbData.Address,
				Token:           hbData.Token,
				CACert:          hbData.CaData,
				CertData:        hbData.CertData,
				KeyData:         hbData.KeyData,
				ProbeNamespaces: hbData.ProbeNamespace,
			},
		},
	}
	err = k8sclient.RestClient.Get(context.Background(), client.ObjectKey{
		Namespace: metav1.NamespaceDefault,
		Name:      hbData.Name,
	}, cluster)

	if apierrors.IsNotFound(err) {
		clusterSpec.ObjectMeta = metav1.ObjectMeta{
			Name:      hbData.Name,
			Namespace: metav1.NamespaceDefault,
		}
		if err = k8sclient.RestClient.Create(context.Background(), &clusterSpec); err != nil {
			errMsg := fmt.Sprintf("[heartbeat] failed to create cluster [%s]: %+v\n", hbData.Name, err)
			rw.Write([]byte(errMsg))
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
	} else {
		patch, _ := json.Marshal(clusterSpec)
		err = k8sclient.RestClient.Patch(context.Background(), &kubeproberv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      hbData.Name,
				Namespace: metav1.NamespaceDefault,
			},
		}, client.RawPatch(types.MergePatchType, patch))
		if err != nil {
			errMsg := fmt.Sprintf("[heartbeat] patch cluster[%s] spec error: %+v\n", hbData.Name, err)
			klog.Errorf(errMsg)
			rw.Write([]byte(errMsg))
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	statusPatchBody := kubeproberv1.Cluster{
		Status: kubeproberv1.ClusterStatus{
			HeartBeatTimeStamp: time.Now().In(loc).Format("2006-01-02 15:04:05"),
			NodeCount:          hbData.NodeCount,
			Checkers:           hbData.Checkers,
			ExtraStatus:        hbData.ExtraStatus,
		},
	}
	fmt.Printf("%v\n", statusPatchBody)
	statusPatch, _ := json.Marshal(statusPatchBody)
	err = k8sclient.RestClient.Status().Patch(context.Background(), &kubeproberv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      hbData.Name,
			Namespace: metav1.NamespaceDefault,
		},
	}, client.RawPatch(types.MergePatchType, statusPatch))
	if err != nil {
		errMsg := fmt.Sprintf("[heartbeat] patch cluster[%s] status error: %+v\n", hbData.Name, err)
		klog.Errorf(errMsg)
		rw.Write([]byte(errMsg))
		rw.WriteHeader(http.StatusBadRequest)
		return
	}
	rw.WriteHeader(http.StatusOK)
	return
}

func Start(ctx context.Context, cfg *Config, influxdbConfig *apistructs.InfluxdbConf) error {
	var dingdingAlert *kubeproberv1.Alert
	var err error
	var client influxdb2.Client
	var writeAPI influxdb2api.WriteAPI

	if influxdbConfig.InfluxdbEnable {
		client = influxdb2.NewClient(influxdbConfig.InfluxdbHost, influxdbConfig.InfluxdbToken)
		writeAPI = client.WriteAPI(influxdbConfig.InfluxdbOrg, influxdbConfig.InfluxdbBucket)
		defer client.Close()
	}

	handler := remotedialer.New(Authorizer, remotedialer.DefaultErrorWriter)
	handler.ClientConnectAuthorizer = func(proto, address string) bool {
		if strings.HasSuffix(proto, "::tcp") {
			return true
		}
		if strings.HasSuffix(proto, "::unix") {
			return address == "/var/run/docker.sock"
		}
		if strings.HasSuffix(proto, "::npipe") {
			return address == "//./pipe/docker_engine"
		}
		return false
	}
	// TODO: support handler.AddPeer
	router := mux.NewRouter()
	router.Handle("/clusterdialer", handler)
	router.Path("/heartbeat").Methods(http.MethodPost).HandlerFunc(heartbeat)
	router.HandleFunc("/clusteragent/connect", func(rw http.ResponseWriter,
		req *http.Request) {
		clusterRegister(handler, rw, req)
	})

	//proxy dingding alert
	if dingdingAlert, err = getDingDingAlert(); err != nil {
		klog.Errorf("failed to get dingding alert crd: %+v\n", err)
	}
	router.HandleFunc("/robot/send", func(rw http.ResponseWriter,
		req *http.Request) {
		dingding.ProxyAlert(rw, req, dingdingAlert)
	})

	router.HandleFunc("/collect", func(rw http.ResponseWriter,
		req *http.Request) {
		collectProbeStatus(rw, req, dingdingAlert, writeAPI)
	})

	router.HandleFunc("/cluster", func(rw http.ResponseWriter,
		req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	router.HandleFunc("/cluster/query", func(rw http.ResponseWriter,
		req *http.Request) {
		httphandler.GetClusterList(rw, req)
	})

	router.HandleFunc("/cluster/search", func(rw http.ResponseWriter,
		req *http.Request) {
		json.NewEncoder(rw).Encode([]string{"NODECOUNT"})
	})

	router.HandleFunc("/alertstatistic", func(rw http.ResponseWriter,
		req *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})

	router.HandleFunc("/alertstatistic/query", func(rw http.ResponseWriter,
		req *http.Request) {
		httphandler.GetAlertStatistic(rw, req)
	})

	router.HandleFunc("/alertstatistic/search", func(rw http.ResponseWriter,
		req *http.Request) {
		json.NewEncoder(rw).Encode([]string{})
	})
	server := &http.Server{
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
		Addr:    cfg.Listen,
		Handler: router,
	}
	return server.ListenAndServe()
}

func getDingDingAlert() (*kubeproberv1.Alert, error) {
	alert := &kubeproberv1.Alert{}
	var err error

	if err = k8sclient.RestClient.Get(context.Background(), client.ObjectKey{
		Namespace: metav1.NamespaceDefault,
		Name:      DINGDING_ALERT_NAME,
	}, alert); err != nil {
		return nil, err
	}

	return alert, nil
}

func collectProbeStatus(rw http.ResponseWriter, req *http.Request, alert *kubeproberv1.Alert, influxdb2api influxdb2api.WriteAPI) {
	ps := apistructs.CollectProbeStatusReq{}
	var err error

	if err = json.NewDecoder(req.Body).Decode(&ps); err != nil {
		errMsg := fmt.Sprintf("receive probe status err: %+v\n", err)
		klog.Errorf(errMsg)
		rw.WriteHeader(http.StatusBadRequest)
		rw.Write([]byte(errMsg))
		return
	}
	if influxdb2api != nil {
		//influxdb2api.WriteRecord(fmt.Sprintf("checker,cluster=%s,checker=%s,probe=%s result=\"%s###%s\"", ps.ClusterName, ps.CheckerName, ps.ProbeName, ps.Status, ps.Message))
		p := influxdb2.NewPointWithMeasurement("checker").
			AddTag("cluster", ps.ClusterName).
			AddTag("checker", ps.CheckerName).
			AddTag("probe", ps.ProbeName).
			AddField("result", fmt.Sprintf("%s###%s", ps.Status, ps.Message)).
			SetTime(time.Now())
		// Flush writes
		influxdb2api.WritePoint(p)
		influxdb2api.Flush()
	}
	if alert.Spec.Token == "" || alert.Spec.Sign == "" {
		return
	}
	if ps.Status == "ERROR" {
		if err = dingding.SendAlert(&ps); err != nil {
			errMsg := fmt.Sprintf("send dingding alert err: %+v\n", err)
			klog.Errorf(errMsg)
		}
	}

	rw.WriteHeader(http.StatusOK)
	return
}
