/*
 * This file is part of the CDI project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2019 Red Hat, Inc.
 *
 */

package apiserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/emicklei/go-restful/v3"

	authorization "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	authorizationclient "k8s.io/client-go/kubernetes/typed/authorization/v1"
	restclient "k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	userHeader            = "X-Remote-User"
	groupHeader           = "X-Remote-Group"
	userExtraHeaderPrefix = "X-Remote-Extra-"
	clientQPS             = 200
	clientBurst           = 400
)

// CdiAPIAuthorizer defines methods to authorize api requests
type CdiAPIAuthorizer interface {
	Authorize(req *restful.Request) (bool, string, error)
}

type authorizor struct {
	authConfigWatcher   AuthConfigWatcher
	subjectAccessReview authorizationclient.SubjectAccessReviewInterface
}

func (a *authorizor) matchHeaders(headers http.Header, toMatch []string) ([]string, error) {
	for _, header := range toMatch {
		value, ok := headers[header]
		if ok {
			return value, nil
		}
	}

	return nil, fmt.Errorf("one of these headers required for authorization: %+v", toMatch)
}

func hasPrefixIgnoreCase(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func unescapeExtraKey(encodedKey string) string {
	key, err := url.PathUnescape(encodedKey) // Decode %-encoded bytes.
	if err != nil {
		return encodedKey // Always record extra strings, even if malformed/unencoded.
	}
	return key
}

func (a *authorizor) getUserExtras(headers http.Header, toMatch []string) map[string]authorization.ExtraValue {
	extras := map[string]authorization.ExtraValue{}

	for _, prefix := range toMatch {
		for k, v := range headers {
			if hasPrefixIgnoreCase(k, prefix) {
				extraKey := unescapeExtraKey(strings.ToLower(k[len(prefix):]))
				extras[extraKey] = v
			}
		}
	}

	return extras
}

// only supporting create for now
var verbMap = map[string]string{
	"POST": "create",
}

func (a *authorizor) generateAccessReview(req *restful.Request) (*authorization.SubjectAccessReview, error) {
	httpRequest := req.Request

	if httpRequest == nil {
		return nil, fmt.Errorf("empty http request")
	}
	headers := httpRequest.Header
	url := httpRequest.URL

	if url == nil {
		return nil, fmt.Errorf("no URL in http request")
	}

	// URL example
	// /apis/upload.cdi.kubevirt.io/v1beta1/namespaces/default/uploadtokenrequest(s)
	pathSplit := strings.Split(url.Path, "/")
	if len(pathSplit) != 7 {
		return nil, fmt.Errorf("unknown api endpoint %s", url.Path)
	}

	authConfig := a.authConfigWatcher.GetAuthConfig()

	group := pathSplit[2]
	version := pathSplit[3]
	namespace := pathSplit[5]
	resource := pathSplit[6]
	userExtras := a.getUserExtras(headers, authConfig.ExtraPrefixHeaders)

	if group != uploadTokenGroup {
		return nil, fmt.Errorf("unknown api group %s", group)
	}

	if resource != "uploadtokenrequests" {
		return nil, fmt.Errorf("unknown resource type %s", resource)
	}

	users, err := a.matchHeaders(headers, authConfig.UserHeaders)
	if err != nil {
		return nil, err
	}

	if len(users) == 0 {
		return nil, fmt.Errorf("no user header found")
	}

	userGroups, err := a.matchHeaders(headers, authConfig.GroupHeaders)
	if err != nil {
		return nil, err
	}

	method := strings.ToUpper(httpRequest.Method)
	verb, exists := verbMap[method]
	if !exists {
		return nil, fmt.Errorf("unsupported HTTP method %s", method)
	}

	r := &authorization.SubjectAccessReview{}
	r.Spec = authorization.SubjectAccessReviewSpec{
		User:   users[0],
		Groups: userGroups,
		Extra:  userExtras,
	}

	klog.V(3).Infof("Generating access review for user %s", r.Spec.User)
	klog.V(3).Infof("Generating access review for groups %v", r.Spec.Groups)
	klog.V(3).Infof("Generating access review for user extras %v", r.Spec.Extra)

	r.Spec.ResourceAttributes = &authorization.ResourceAttributes{
		Namespace: namespace,
		Verb:      verb,
		Group:     group,
		Version:   version,
		Resource:  resource,
	}

	return r, nil
}

func isInfoEndpoint(req *restful.Request) bool {
	httpRequest := req.Request
	if httpRequest == nil || httpRequest.URL == nil {
		return false
	}
	// URL example
	// /apis/upload.cdi.kubevirt.io/v1alpha2/namespaces/default/uploadtokenrequests/test
	// The /apis/<group>/<version> part of the urls should be accessible without needing authorization
	pathSplit := strings.Split(httpRequest.URL.Path, "/")
	if len(pathSplit) <= 4 || (len(pathSplit) > 4 && pathSplit[4] == "version") {
		return true
	}

	return false
}

func isAuthenticated(req *restful.Request) bool {
	klog.V(3).Infof("Authenticating request: %+v", req.Request)
	klog.V(3).Infof("Authenticating request TLS: %+v", req.Request.TLS)

	// Peer cert is required for authentication.
	// If the peer's cert is provided, we are guaranteed
	// it has been validated against our client CA pool
	if req.Request == nil ||
		req.Request.TLS == nil ||
		len(req.Request.TLS.PeerCertificates) == 0 ||
		len(req.Request.TLS.VerifiedChains) == 0 {
		return false
	}
	return true
}

func isAllowed(result *authorization.SubjectAccessReview) (bool, string) {
	if result.Status.Allowed {
		return true, ""
	}

	return false, result.Status.Reason
}

func (a *authorizor) Authorize(req *restful.Request) (bool, string, error) {
	// Endpoints related to getting information about
	// what apis our server provides are authorized to
	// all users.
	if isInfoEndpoint(req) {
		return true, "", nil
	}

	if !isAuthenticated(req) {
		return false, "request is not authenticated", nil
	}

	r, err := a.generateAccessReview(req)
	if err != nil {
		// only internal service errors are returned
		// as an error.
		// A failure to generate the access review request
		// means the client did not properly format the request.
		// Return that error as the "Reason" for the authorization failure.
		return false, fmt.Sprintf("%v", err), nil
	}

	result, err := a.subjectAccessReview.Create(context.TODO(), r, metav1.CreateOptions{})
	if err != nil {
		return false, "internal server error", err
	}

	allowed, reason := isAllowed(result)
	return allowed, reason, nil
}

// NewAuthorizorFromConfig creates a new CdiAPIAuthorizor
func NewAuthorizorFromConfig(config *restclient.Config, authConfigWatcher AuthConfigWatcher) (CdiAPIAuthorizer, error) {
	client, err := authorizationclient.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	subjectAccessReview := client.SubjectAccessReviews()

	a := &authorizor{
		authConfigWatcher:   authConfigWatcher,
		subjectAccessReview: subjectAccessReview,
	}

	return a, nil
}
