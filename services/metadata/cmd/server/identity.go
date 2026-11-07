package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/wuhy80/AgentForge-Ontology/contracts"
	"github.com/wuhy80/AgentForge-Ontology/libs/platform"
	"github.com/wuhy80/AgentForge-Ontology/services/metadata/identity"
)

func internalIdentityCall(service string, action func(http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !platform.EqualSecret(platform.Bearer(r), service) {
			platform.WriteError(w, 401, "service_unauthenticated", "需要有效服务身份")
			return
		}
		action(w, r)
	})
}

func decodeIdentity(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		platform.WriteError(w, 400, "invalid_request", "请求格式无效")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		platform.WriteError(w, 400, "invalid_request", "请求只能包含一个对象")
		return false
	}
	return true
}

func writeIdentityError(w http.ResponseWriter, err error) {
	var access *platform.AccessError
	if errors.As(err, &access) {
		platform.WriteError(w, access.Status, access.Code, access.Message)
		return
	}
	platform.WriteError(w, 503, "identity_unavailable", "用户或授权服务暂不可用")
}

func internalSession(service string, store *identity.Store) http.Handler {
	return internalIdentityCall(service, func(w http.ResponseWriter, r *http.Request) {
		var check contracts.SessionCheck
		if !decodeIdentity(w, r, &check) {
			return
		}
		session, err := store.CheckSession(r.Context(), check)
		if err != nil {
			writeIdentityError(w, err)
			return
		}
		platform.WriteJSON(w, 200, session)
	})
}

func internalDecision(service string, store *identity.Store) http.Handler {
	return internalIdentityCall(service, func(w http.ResponseWriter, r *http.Request) {
		var check contracts.AuthorizationCheck
		if !decodeIdentity(w, r, &check) {
			return
		}
		decision, err := store.Authorize(r.Context(), check.Principal, check.Request)
		if err != nil {
			writeIdentityError(w, err)
			return
		}
		platform.WriteJSON(w, 200, decision)
	})
}
