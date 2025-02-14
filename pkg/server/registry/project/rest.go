/*
 *
 *  * Copyright 2021 KubeClipper Authors.
 *  *
 *  * Licensed under the Apache License, Version 2.0 (the "License");
 *  * you may not use this file except in compliance with the License.
 *  * You may obtain a copy of the License at
 *  *
 *  *     http://www.apache.org/licenses/LICENSE-2.0
 *  *
 *  * Unless required by applicable law or agreed to in writing, software
 *  * distributed under the License is distributed on an "AS IS" BASIS,
 *  * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *  * See the License for the specific language governing permissions and
 *  * limitations under the License.
 *
 */

// Package project implement Project's StandardStorage
package project

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/generic"
	genericregistry "k8s.io/apiserver/pkg/registry/generic/registry"
	"k8s.io/apiserver/pkg/registry/rest"

	v1 "github.com/kubeclipper/kubeclipper/pkg/scheme/tenant/v1"
)

// NewStorage return a StandardStorage
func NewStorage(scheme *runtime.Scheme, optsGetter generic.RESTOptionsGetter) (rest.StandardStorage, error) {
	strategy := NewStrategy(scheme)

	store := &genericregistry.Store{
		NewFunc: func() runtime.Object {
			return &v1.Project{}
		},
		NewListFunc: func() runtime.Object {
			return &v1.ProjectList{}
		},
		DefaultQualifiedResource: v1.Resource("projects"),
		KeyRootFunc:              nil,
		KeyFunc:                  nil,
		ObjectNameFunc:           nil,
		TTLFunc:                  nil,
		PredicateFunc:            nil,
		EnableGarbageCollection:  false,
		DeleteCollectionWorkers:  0,
		Decorator:                nil,
		CreateStrategy:           strategy,
		BeginCreate:              nil,
		AfterCreate:              nil,
		UpdateStrategy:           strategy,
		BeginUpdate:              nil,
		AfterUpdate:              nil,
		DeleteStrategy:           strategy,
		AfterDelete:              nil,
		ReturnDeletedObject:      false,
		ShouldDeleteDuringUpdate: nil,
		TableConvertor:           rest.NewDefaultTableConvertor(v1.Resource("projects")),
		ResetFieldsStrategy:      nil,
		Storage:                  genericregistry.DryRunnableStorage{},
		StorageVersioner:         nil,
		DestroyFunc:              nil,
	}
	options := &generic.StoreOptions{RESTOptions: optsGetter, AttrFunc: GetAttrs}
	if err := store.CompleteWithOptions(options); err != nil {
		return nil, err
	}
	return store, nil
}
