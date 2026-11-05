package main

import (
	"os"
	"sync"

	"github.com/winterstim/tonelab-muse/pkg/muse"
)

type runtimeKeys struct {
	mu      sync.RWMutex
	meta    *muse.Client
	metaKey string
	models  map[string]string
}

func newRuntimeKeys(meta *muse.Client) *runtimeKeys {
	return &runtimeKeys{meta: meta, models: make(map[string]string)}
}

func (k *runtimeKeys) SetModelAPIKey(key string) {
	if os.Getenv("MODEL_API_KEY") != "" {
		return
	}
	k.mu.Lock()
	k.metaKey = key
	k.meta.SetKey(key)
	k.mu.Unlock()
}

func (k *runtimeKeys) ModelAPIKeySet() bool {
	if os.Getenv("MODEL_API_KEY") != "" {
		return true
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.metaKey != ""
}

func (k *runtimeKeys) replaceMetaKey(key string) (old string, existed bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	old, existed = k.metaKey, k.metaKey != ""
	if os.Getenv("MODEL_API_KEY") == "" {
		k.metaKey = key
		k.meta.SetKey(key)
	}
	return old, existed
}

func (k *runtimeKeys) restoreMetaKey(key string, existed bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if existed {
		k.metaKey = key
	} else {
		k.metaKey = ""
	}
	if os.Getenv("MODEL_API_KEY") == "" {
		k.meta.SetKey(k.metaKey)
	}
}

func (k *runtimeKeys) ModelKey(name string) string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.models[name]
}

func (k *runtimeKeys) replaceModelKey(name, key string) (old string, existed bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	old, existed = k.models[name]
	if key == "" {
		delete(k.models, name)
	} else {
		k.models[name] = key
	}
	return old, existed
}

func (k *runtimeKeys) restoreModelKey(name, key string, existed bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if existed {
		k.models[name] = key
	} else {
		delete(k.models, name)
	}
}
