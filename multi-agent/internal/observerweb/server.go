package observerweb

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/yourorg/multi-agent/internal/commanderhub"
	"github.com/yourorg/multi-agent/internal/commanderhub/authstore"
	"github.com/yourorg/multi-agent/internal/identity"
	"github.com/yourorg/multi-agent/internal/identity/static"
	"github.com/yourorg/multi-agent/internal/objectstore"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/internal/userspace"
)

const defaultMaxEventBodyBytes = 256 << 10
const defaultObjectPresignTTL = 15 * time.Minute
const defaultMaxObjectProxyBytes = 8 << 20

// recentRegisterWindow is how recent a (workspace, agent_id) registration
// must be before the register handler refuses a duplicate without
// force=true. See §1.3 #11 of docs/review-2026-06-13.md.
const recentRegisterWindow = 5 * time.Minute

var errObjectProxyTooLarge = errors.New("object proxy content too large")

type Store = observerstore.Store

type RateLimitConfig struct {
	PerMinute int
	Burst     int
}

type Options struct {
	TelemetryRateLimit  RateLimitConfig
	MaxEventBodyBytes   int64
	Objects             objectstore.Store
	DisableObjectProxy  bool
	MaxObjectProxyBytes int64
	RegisterDisabled    bool
	AgentserverURL      string // PR-3: enables /commander surface (empty = disabled)

	// AuthStore is the commander login/session persistence layer. REQUIRED
	// when AgentserverURL != "". NewWithResolverOptions panics if
	// AgentserverURL is set and AuthStore is nil — silent in-memory fallback
	// would re-introduce the multi-pod login bug this package was built to fix.
	AuthStore authstore.Store
}

// New constructs the observerweb HTTP handler. If usHandler is non-nil,
// /api/userspace/* routes are mounted on the same mux.
func New(s Store, usHandler *userspace.Handler) http.Handler {
	return NewWithResolverOptions(s, usHandler, static.New(s), Options{
		TelemetryRateLimit: RateLimitConfig{PerMinute: 60, Burst: 120},
		MaxEventBodyBytes:  defaultMaxEventBodyBytes,
	})
}

func NewWithOptions(s Store, usHandler *userspace.Handler, opts Options) http.Handler {
	return NewWithResolverOptions(s, usHandler, static.New(s), opts)
}

func NewWithResolver(s Store, usHandler *userspace.Handler, resolver identity.Resolver) http.Handler {
	return NewWithResolverOptions(s, usHandler, resolver, Options{})
}

func NewWithResolverOptions(s Store, usHandler *userspace.Handler, resolver identity.Resolver, opts Options) http.Handler {
	if resolver == nil {
		resolver = static.New(s)
	}
	if opts.TelemetryRateLimit.PerMinute == 0 {
		opts.TelemetryRateLimit.PerMinute = 60
	}
	if opts.TelemetryRateLimit.Burst == 0 {
		opts.TelemetryRateLimit.Burst = 120
	}
	if opts.MaxEventBodyBytes == 0 {
		opts.MaxEventBodyBytes = defaultMaxEventBodyBytes
	}
	if opts.MaxObjectProxyBytes <= 0 {
		opts.MaxObjectProxyBytes = defaultMaxObjectProxyBytes
	}
	h := &handler{
		s:                   s,
		resolver:            resolver,
		registerEnabled:     !opts.RegisterDisabled,
		objects:             opts.Objects,
		objectProxyEnabled:  !opts.DisableObjectProxy,
		telemetryLimiter:    newTelemetryLimiter(opts.TelemetryRateLimit.PerMinute, opts.TelemetryRateLimit.Burst),
		maxEventBodyBytes:   opts.MaxEventBodyBytes,
		maxObjectProxyBytes: opts.MaxObjectProxyBytes,
	}
	mux := http.NewServeMux()
	mountRoutes(mux, h, usHandler)
	if opts.AgentserverURL != "" {
		if opts.AuthStore == nil {
			panic("observerweb: AuthStore is required when AgentserverURL is set (see internal/commanderhub/authstore)")
		}
		commanderhub.MountAll(mux, resolver, opts.AgentserverURL, opts.AuthStore)
	}
	return mux
}

func mountRoutes(mux *http.ServeMux, h *handler, usHandler *userspace.Handler) {
	// Ingest only. The legacy GET /api/events read endpoint was removed when
	// the unauthenticated dashboard came down; agents that need to replay
	// events must use a tokened endpoint we haven't built yet.
	mux.HandleFunc("/api/events", h.postEvent)
	mux.HandleFunc("/api/agents/register", h.register)
	mux.HandleFunc("/api/tasks/", h.taskRouter)
	mux.HandleFunc("/api/artifacts", h.artifacts)
	mux.HandleFunc("/api/artifacts/", h.artifactRouter)
	mux.HandleFunc("/api/artifact-requests", h.artifactRequests)
	mux.HandleFunc("/api/write-tokens", h.writeTokens)
	mux.HandleFunc("/api/writes", h.writes)
	mux.HandleFunc("/api/writes/", h.writeRouter)
	mux.HandleFunc("/api/task-contracts", h.taskContracts)
	mux.HandleFunc("/api/task-contracts/", h.taskContractByID)
	mux.HandleFunc("/api/resource-snapshots", h.resourceSnapshots)
	mux.HandleFunc("/api/resource-snapshots/latest", h.latestResourceSnapshot)
	mux.HandleFunc("/api/workspaces", h.guardWebToken(h.listWorkspaces))
	if usHandler != nil {
		userspace.MountRoutes(mux, usHandler)
	}
}

type handler struct {
	s                   Store
	resolver            identity.Resolver
	registerEnabled     bool
	objects             objectstore.Store
	objectProxyEnabled  bool
	telemetryLimiter    telemetryAllower
	maxEventBodyBytes   int64
	maxObjectProxyBytes int64
}

func (h *handler) postEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ident, ok := h.identityFromRequest(w, r)
	if !ok {
		return
	}
	agent := agentFromIdentity(ident)

	telemetryHeader := strings.TrimSpace(r.Header.Get("X-Loom-Telemetry-Key"))
	if telemetryHeader == "" {
		http.Error(w, "missing telemetry api key", http.StatusForbidden)
		return
	}
	telemetryKeyID, ok, err := h.s.LookupTelemetryAPIKey(telemetryHeader, agent.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "invalid telemetry api key", http.StatusForbidden)
		return
	}

	var ev observer.Event
	r.Body = http.MaxBytesReader(w, r.Body, h.maxEventBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&ev); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	var trailing struct{}
	if err := dec.Decode(&trailing); err != io.EOF {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if ev.WorkspaceID != agent.WorkspaceID || ev.AgentID != agent.ID || ev.AgentRole != agent.Role {
		http.Error(w, "workspace or agent mismatch", http.StatusForbidden)
		return
	}
	if h.telemetryLimiter != nil {
		key := telemetryKey{
			WorkspaceID:    agent.WorkspaceID,
			AgentID:        agent.ID,
			TelemetryKeyID: telemetryKeyID,
		}
		allowed, err := h.telemetryLimiter.allow(key, time.Now())
		if err != nil {
			http.Error(w, "telemetry rate limit unavailable", http.StatusServiceUnavailable)
			log.Printf("observerweb: telemetry rate limit error: %v", err)
			return
		}
		if !allowed {
			http.Error(w, "telemetry rate limit exceeded", http.StatusTooManyRequests)
			return
		}
	}
	if err := h.recordExternalIdentity(ident); err != nil {
		log.Printf("observer: RecordExternalIdentity error ws=%s id=%s: %v", ident.WorkspaceID, ident.AgentID, err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if err := h.s.Ingest(ev); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func bearerToken(auth string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	return token, token != ""
}

// AgentFromRequest validates the Bearer token and returns the authenticated
// agent's workspace_id + agent_id. ok=false means anonymous or invalid.
// Sibling packages (internal/userspace) call this when mounting routes on
// the same mux so token validation lives in one place.
func AgentFromRequest(s Store, r *http.Request) (string, string, bool) {
	return AgentFromRequestWithResolver(static.New(s), r)
}

func AgentFromRequestWithResolver(resolver identity.Resolver, r *http.Request) (string, string, bool) {
	ident, ok := IdentityFromRequest(resolver, r)
	if !ok {
		return "", "", false
	}
	return ident.WorkspaceID, ident.AgentID, true
}

func IdentityFromRequest(resolver identity.Resolver, r *http.Request) (identity.Identity, bool) {
	tok, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		return identity.Identity{}, false
	}
	ident, err := resolver.Resolve(r.Context(), tok)
	if err != nil {
		return identity.Identity{}, false
	}
	return ident, true
}

func (h *handler) authenticate(w http.ResponseWriter, r *http.Request) (observerstore.Agent, bool) {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		token = strings.TrimSpace(r.URL.Query().Get("token"))
		ok = token != ""
		if !ok {
			http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
			return observerstore.Agent{}, false
		}
	}
	ident, ok := h.identityFromToken(w, r, token)
	if !ok {
		return observerstore.Agent{}, false
	}
	if err := h.recordExternalIdentity(ident); err != nil {
		log.Printf("observer: RecordExternalIdentity error ws=%s id=%s: %v", ident.WorkspaceID, ident.AgentID, err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return observerstore.Agent{}, false
	}
	return agentFromIdentity(ident), true
}

func (h *handler) identityFromRequest(w http.ResponseWriter, r *http.Request) (identity.Identity, bool) {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
		return identity.Identity{}, false
	}
	return h.identityFromToken(w, r, token)
}

func (h *handler) identityFromToken(w http.ResponseWriter, r *http.Request, token string) (identity.Identity, bool) {
	ident, err := h.resolver.Resolve(r.Context(), token)
	if err != nil {
		writeIdentityError(w, err)
		return identity.Identity{}, false
	}
	return ident, true
}

func (h *handler) recordExternalIdentity(ident identity.Identity) error {
	if ident.Source != identity.SourceAgentserver {
		return nil
	}
	return h.s.RecordExternalIdentity(observerstore.Agent{
		WorkspaceID:       ident.WorkspaceID,
		ID:                ident.AgentID,
		Role:              ident.Role,
		DisplayName:       ident.AgentID,
		ExternalSandboxID: ident.SandboxID,
		ExternalUserID:    ident.UserID,
	}, ident.WorkspaceName)
}

func agentFromIdentity(ident identity.Identity) observerstore.Agent {
	return observerstore.Agent{
		WorkspaceID:       ident.WorkspaceID,
		ID:                ident.AgentID,
		Role:              ident.Role,
		DisplayName:       ident.AgentID,
		ExternalSandboxID: ident.SandboxID,
		ExternalUserID:    ident.UserID,
	}
}

func writeIdentityError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrInvalid):
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	case errors.Is(err, identity.ErrRevoked):
		http.Error(w, "token revoked", http.StatusForbidden)
	case errors.Is(err, identity.ErrUpstream):
		w.Header().Set("Retry-After", "5")
		http.Error(w, "identity upstream unavailable", http.StatusServiceUnavailable)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func absoluteURL(r *http.Request, path string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
		scheme = strings.Split(xf, ",")[0]
	}
	return scheme + "://" + r.Host + path
}

func (h *handler) artifacts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var req struct {
		Path   string `json:"path"`
		Kind   string `json:"kind"`
		MIME   string `json:"mime"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
		Mode   string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Kind == "" {
		req.Kind = "file"
	}
	art, err := h.s.CreateArtifact(observerstore.ArtifactCreate{
		WorkspaceID: agent.WorkspaceID, OwnerAgentID: agent.ID,
		Path: req.Path, Kind: req.Kind, MIME: req.MIME, Bytes: req.Bytes, SHA256: req.SHA256,
		State: observerstore.ArtifactStateRegistered,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := map[string]interface{}{
		"artifact_id": art.ID,
		"state":       art.State,
		"url":         absoluteURL(r, "/api/artifacts/"+url.PathEscape(art.ID)),
	}
	if h.objects != nil {
		if h.objectProxyEnabled {
			resp["put_url"] = absoluteURL(r, "/api/artifacts/"+url.PathEscape(art.ID)+"/content")
		} else {
			objectKey := objectstore.ArtifactKey(agent.WorkspaceID, art.ID)
			putURL, err := h.objects.PutPresignedURL(r.Context(), objectKey, req.MIME, defaultObjectPresignTTL)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			resp["put_url"] = putURL
			resp["object_key"] = objectKey
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func (h *handler) artifactRouter(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/artifacts/")
	if strings.HasSuffix(rest, "/complete") {
		id := strings.TrimSuffix(rest, "/complete")
		h.completeArtifact(w, r, id)
		return
	}
	if strings.HasSuffix(rest, "/content") {
		id := strings.TrimSuffix(rest, "/content")
		h.putArtifactContent(w, r, id)
		return
	}
	if strings.HasSuffix(rest, "/list") || strings.HasSuffix(rest, "/blob") {
		// Directory lazy list/blob requests use the same pending request path in
		// v1; rel paths are carried by the URL query for driver-side resolution.
		id := strings.TrimSuffix(strings.TrimSuffix(rest, "/list"), "/blob")
		h.getArtifact(w, r, id)
		return
	}
	h.getArtifact(w, r, rest)
}

func (h *handler) getArtifact(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	content, err := h.s.OpenArtifactContent(agent.WorkspaceID, id)
	if err == nil {
		if content.ObjectKey != "" {
			defer content.Body.Close()
			if h.objects == nil {
				http.Error(w, "object store not configured", http.StatusInternalServerError)
				return
			}
			if !h.objectProxyEnabled {
				h.writeArtifactObjectHandle(w, r, content)
				return
			}
			body, err := h.objects.Open(r.Context(), content.ObjectKey)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			data, err := h.readObjectProxy(body)
			closeErr := body.Close()
			if err != nil {
				writeObjectProxyError(w, err)
				return
			}
			if closeErr != nil {
				http.Error(w, closeErr.Error(), http.StatusInternalServerError)
				return
			}
			if content.MIME != "" {
				w.Header().Set("Content-Type", content.MIME)
			}
			_, _ = w.Write(data)
			return
		}
		if content.MIME != "" {
			w.Header().Set("Content-Type", content.MIME)
		}
		defer content.Body.Close()
		io.Copy(w, content.Body) //nolint:errcheck
		return
	}
	req, err := h.s.RequestArtifact(agent.WorkspaceID, agent.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "2")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"state":       req.State,
		"artifact_id": req.ArtifactID,
		"request_id":  req.RequestID,
	})
}

func (h *handler) putArtifactContent(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if h.objects != nil {
		if !h.objectProxyEnabled {
			http.Error(w, "object proxy disabled", http.StatusForbidden)
			return
		}
		objectKey := objectstore.ArtifactKey(agent.WorkspaceID, id)
		body := http.MaxBytesReader(w, r.Body, h.maxObjectProxyBytes)
		info, err := h.objects.Put(r.Context(), objectKey, r.Header.Get("Content-Type"), body)
		if err != nil {
			_ = h.objects.Delete(r.Context(), objectKey)
			writeObjectProxyError(w, err)
			return
		}
		if err := h.s.MarkArtifactAvailable(agent.WorkspaceID, agent.ID, id, r.Header.Get("Content-Type"), info.SHA256, objectKey, info.Bytes); err != nil {
			delErr := h.objects.Delete(r.Context(), objectKey)
			if delErr != nil {
				log.Printf("observer: ORPHAN OBJECT %s after DB MarkArtifactAvailable failed: db_err=%v delete_err=%v",
					objectKey, err, delErr)
			} else {
				log.Printf("observer: rolled back object %s after DB MarkArtifactAvailable failed: %v",
					objectKey, err)
			}
			// Classify: only ErrArtifactNotFound is "wrong target" (404). Any
			// other error is a server-side problem (DB unreachable, constraint
			// violation, etc.) — 502 so the uploading client retries rather
			// than assumes its target ID was bad. Fixes §1.3 #10 of
			// docs/review-2026-06-13.md.
			if errors.Is(err, observerstore.ErrArtifactNotFound) {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := h.s.StoreArtifactContent(agent.WorkspaceID, agent.ID, id, r.Header.Get("Content-Type"), r.Body); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) completeArtifact(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if h.objects == nil {
		http.Error(w, "object store not configured", http.StatusInternalServerError)
		return
	}
	if h.objectProxyEnabled {
		http.Error(w, "object proxy enabled", http.StatusForbidden)
		return
	}
	var req struct {
		MIME      string `json:"mime"`
		Bytes     int64  `json:"bytes"`
		SHA256    string `json:"sha256"`
		ObjectKey string `json:"object_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Bytes < 0 {
		http.Error(w, "bytes must be non-negative", http.StatusBadRequest)
		return
	}
	objectKey := objectstore.ArtifactKey(agent.WorkspaceID, id)
	if req.ObjectKey != "" && req.ObjectKey != objectKey {
		http.Error(w, "object_key does not match artifact", http.StatusBadRequest)
		return
	}
	if err := h.s.MarkArtifactAvailable(agent.WorkspaceID, agent.ID, id, req.MIME, req.SHA256, objectKey, req.Bytes); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	h.writeArtifactObjectHandle(w, r, observerstore.ArtifactContent{
		Artifact: observerstore.Artifact{
			ID: id, WorkspaceID: agent.WorkspaceID, OwnerAgentID: agent.ID,
			MIME: req.MIME, State: observerstore.ArtifactStateAvailable,
			Bytes: req.Bytes, SHA256: req.SHA256, ObjectKey: objectKey,
		},
	})
}

func (h *handler) writeArtifactObjectHandle(w http.ResponseWriter, r *http.Request, content observerstore.ArtifactContent) {
	getURL, err := h.objects.GetPresignedURL(r.Context(), content.ObjectKey, defaultObjectPresignTTL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"artifact_id": content.ID,
		"path":        content.Path,
		"kind":        content.Kind,
		"state":       content.State,
		"mime":        content.MIME,
		"bytes":       content.Bytes,
		"sha256":      content.SHA256,
		"object_key":  content.ObjectKey,
		"get_url":     getURL,
	})
}

func (h *handler) artifactRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	reqs, err := h.s.ListArtifactRequests(agent.WorkspaceID, agent.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if reqs == nil {
		reqs = []observerstore.ArtifactRequest{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"requests": reqs})
}

func (h *handler) writeTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var req struct {
		TaskID    string `json:"task_id"`
		Path      string `json:"path"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	wr, err := h.s.CreateWrite(observerstore.WriteCreate{
		WorkspaceID: agent.WorkspaceID, OwnerAgentID: agent.ID,
		TaskID: req.TaskID, Path: req.Path, Overwrite: req.Overwrite,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := map[string]interface{}{
		"write_id": wr.ID,
		"put_url":  absoluteURL(r, "/api/writes/"+url.PathEscape(wr.ID)),
	}
	if h.objects != nil && !h.objectProxyEnabled {
		objectKey := objectstore.WriteKey(agent.WorkspaceID, wr.ID)
		putURL, err := h.objects.PutPresignedURL(r.Context(), objectKey, "", defaultObjectPresignTTL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp["object_put_url"] = putURL
		resp["object_key"] = objectKey
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func (h *handler) writeRouter(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/writes/")
	if strings.HasSuffix(rest, "/complete") {
		id := strings.TrimSuffix(rest, "/complete")
		h.completeWrite(w, r, id)
		return
	}
	if r.Method == http.MethodPatch {
		h.patchWrite(w, r)
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	id := rest
	if h.objects != nil {
		if !h.objectProxyEnabled {
			http.Error(w, "object proxy disabled", http.StatusForbidden)
			return
		}
		objectKey := objectstore.WriteKey(agent.WorkspaceID, id)
		body := http.MaxBytesReader(w, r.Body, h.maxObjectProxyBytes)
		info, err := h.objects.Put(r.Context(), objectKey, r.Header.Get("Content-Type"), body)
		if err != nil {
			_ = h.objects.Delete(r.Context(), objectKey)
			writeObjectProxyError(w, err)
			return
		}
		if err := h.s.MarkWriteCompleted(agent.WorkspaceID, agent.ID, id, r.Header.Get("Content-Type"), info.SHA256, objectKey, info.Bytes); err != nil {
			delErr := h.objects.Delete(r.Context(), objectKey)
			if delErr != nil {
				log.Printf("observer: ORPHAN OBJECT %s after DB MarkWriteCompleted failed: db_err=%v delete_err=%v",
					objectKey, err, delErr)
			} else {
				log.Printf("observer: rolled back object %s after DB MarkWriteCompleted failed: %v",
					objectKey, err)
			}
			if errors.Is(err, observerstore.ErrWriteNotFound) {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := h.s.StoreWriteContent(agent.WorkspaceID, agent.ID, id, r.Header.Get("Content-Type"), r.Body); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) completeWrite(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if h.objects == nil {
		http.Error(w, "object store not configured", http.StatusInternalServerError)
		return
	}
	if h.objectProxyEnabled {
		http.Error(w, "object proxy enabled", http.StatusForbidden)
		return
	}
	var req struct {
		MIME      string `json:"mime"`
		Bytes     int64  `json:"bytes"`
		SHA256    string `json:"sha256"`
		ObjectKey string `json:"object_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Bytes < 0 {
		http.Error(w, "bytes must be non-negative", http.StatusBadRequest)
		return
	}
	objectKey := objectstore.WriteKey(agent.WorkspaceID, id)
	if req.ObjectKey != "" && req.ObjectKey != objectKey {
		http.Error(w, "object_key does not match write", http.StatusBadRequest)
		return
	}
	if err := h.s.MarkWriteCompleted(agent.WorkspaceID, agent.ID, id, req.MIME, req.SHA256, objectKey, req.Bytes); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	getURL, err := h.objects.GetPresignedURL(r.Context(), objectKey, defaultObjectPresignTTL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"write_id":       id,
		"state":          observerstore.WriteStateCompleted,
		"mime":           req.MIME,
		"bytes":          req.Bytes,
		"sha256":         req.SHA256,
		"object_key":     objectKey,
		"object_get_url": getURL,
	})
}

func (h *handler) patchWrite(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var req struct {
		TaskID string `json:"task_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/writes/")
	if err := h.s.UpdateWriteTaskID(agent.WorkspaceID, agent.ID, id, req.TaskID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) writes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	taskID := r.URL.Query().Get("task_id")
	rows, err := h.s.ListCompletedWrites(agent.WorkspaceID, agent.ID, taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []observerstore.Write{}
	}
	type writeResponse struct {
		WriteID       string `json:"write_id"`
		Path          string `json:"path"`
		Overwrite     bool   `json:"overwrite"`
		State         string `json:"state"`
		MIME          string `json:"mime,omitempty"`
		Bytes         int64  `json:"bytes,omitempty"`
		SHA256        string `json:"sha256,omitempty"`
		ObjectKey     string `json:"object_key,omitempty"`
		ObjectGetURL  string `json:"object_get_url,omitempty"`
		Content       []byte `json:"content"`
		WriterAgentID string `json:"writer_agent_id,omitempty"`
	}
	resp := make([]writeResponse, len(rows))
	for i, row := range rows {
		content := row.Content
		var objectGetURL string
		if row.ObjectKey != "" {
			if h.objects == nil {
				http.Error(w, "object store not configured", http.StatusInternalServerError)
				return
			}
			objectGetURL, err = h.objects.GetPresignedURL(r.Context(), row.ObjectKey, defaultObjectPresignTTL)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if !h.objectProxyEnabled {
				content = nil
				resp[i] = writeResponse{
					WriteID: row.ID, Path: row.Path, Overwrite: row.Overwrite,
					State: row.State, MIME: row.MIME, Bytes: row.Bytes, SHA256: row.SHA256,
					ObjectKey: row.ObjectKey, ObjectGetURL: objectGetURL, Content: content, WriterAgentID: row.WriterAgentID,
				}
				continue
			}
			body, err := h.objects.Open(r.Context(), row.ObjectKey)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			content, err = h.readObjectProxy(body)
			closeErr := body.Close()
			if err != nil {
				writeObjectProxyError(w, err)
				return
			}
			if closeErr != nil {
				http.Error(w, closeErr.Error(), http.StatusInternalServerError)
				return
			}
		}
		resp[i] = writeResponse{
			WriteID: row.ID, Path: row.Path, Overwrite: row.Overwrite,
			State: row.State, MIME: row.MIME, Bytes: row.Bytes, SHA256: row.SHA256,
			ObjectKey: row.ObjectKey, ObjectGetURL: objectGetURL, Content: content, WriterAgentID: row.WriterAgentID,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"writes": resp})
}

func (h *handler) readObjectProxy(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, h.maxObjectProxyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > h.maxObjectProxyBytes {
		return nil, errObjectProxyTooLarge
	}
	return data, nil
}

func writeObjectProxyError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.Is(err, errObjectProxyTooLarge) || errors.As(err, &maxBytesErr) {
		http.Error(w, "object proxy content too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func (h *handler) taskContracts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if agent.Role != observer.RoleDriver {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		TaskID         string          `json:"task_id"`
		ConversationID string          `json:"conversation_id"`
		Body           json.RawMessage `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	record := observerstore.TaskContractRecord{
		WorkspaceID: agent.WorkspaceID, OwnerAgentID: agent.ID,
		TaskID: req.TaskID, ConversationID: req.ConversationID, Body: req.Body,
	}
	if err := h.s.SaveTaskContract(record); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	got, err := h.s.GetTaskContract(agent.WorkspaceID, req.TaskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(got)
}

func (h *handler) taskContractByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	taskID := strings.TrimPrefix(r.URL.Path, "/api/task-contracts/")
	got, err := h.s.GetTaskContract(agent.WorkspaceID, taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if got.OwnerAgentID != agent.ID && agent.Role != observer.RoleMaster {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(got)
}

func (h *handler) resourceSnapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if agent.Role != observer.RoleDriver && agent.Role != observer.RoleMaster {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		SnapshotID string          `json:"snapshot_id"`
		Body       json.RawMessage `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	record := observerstore.ResourceSnapshotRecord{
		WorkspaceID: agent.WorkspaceID, OwnerAgentID: agent.ID,
		SnapshotID: req.SnapshotID, Body: req.Body,
	}
	if err := h.s.SaveResourceSnapshot(record); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(record)
}

func (h *handler) latestResourceSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if agent.Role != observer.RoleDriver && agent.Role != observer.RoleMaster {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	got, err := h.s.GetLatestResourceSnapshot(agent.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(got)
}

// taskRouter handles GET /api/tasks/{task_id}/progress. The legacy
// unauthenticated GET /api/tasks (full-list) endpoint was removed; only this
// tokened, per-(workspace, task) lookup remains for driver's awaiting_user
// marker recovery path.
func (h *handler) taskRouter(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
	if !strings.HasSuffix(rest, "/progress") {
		http.NotFound(w, r)
		return
	}
	taskID := strings.TrimSuffix(rest, "/progress")
	if taskID == "" || strings.Contains(taskID, "/") {
		http.Error(w, "bad task id", http.StatusBadRequest)
		return
	}
	h.getTaskProgress(w, r, taskID)
}

func (h *handler) getTaskProgress(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	prog, found, err := h.s.GetTaskProgress(agent.WorkspaceID, taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(prog)
}

type registerRequest struct {
	AgentID       string `json:"agent_id"`
	Role          string `json:"role"`
	DisplayName   string `json:"display_name"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	Force         bool   `json:"force,omitempty"`
}

type registerResponse struct {
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	Token       string `json:"token"`
}

var agentIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var workspaceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (h *handler) register(w http.ResponseWriter, r *http.Request) {
	if !h.registerEnabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	apiKey, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
		return
	}
	keyID, ok, err := h.s.LookupAPIKey(apiKey)
	if err != nil {
		log.Printf("observer: LookupAPIKey error: %v", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "invalid api key", http.StatusUnauthorized)
		return
	}

	var req registerRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<14)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	var trailing struct{}
	if err := dec.Decode(&trailing); err != io.EOF {
		http.Error(w, "bad json: trailing content", http.StatusBadRequest)
		return
	}

	if !agentIDPattern.MatchString(req.AgentID) {
		http.Error(w, "agent_id must match [A-Za-z0-9_-]{1,64}", http.StatusBadRequest)
		return
	}
	if !workspaceIDPattern.MatchString(req.WorkspaceID) {
		http.Error(w, "workspace_id must match [A-Za-z0-9_-]{1,64}", http.StatusBadRequest)
		return
	}
	if !validRegisterRole(req.Role) {
		http.Error(w, "role must be one of driver, master, slave", http.StatusBadRequest)
		return
	}
	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.AgentID
	}

	// Early rebind check, BEFORE any DB write — protects against orphan workspaces.
	if existing, found, err := h.s.AgentBoundWorkspace(req.AgentID); err != nil {
		log.Printf("observer: AgentBoundWorkspace error: %v", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	} else if found && existing != req.WorkspaceID {
		http.Error(w,
			fmt.Sprintf("agent already bound to workspace %s", existing),
			http.StatusConflict)
		return
	}

	// Duplicate-takeover guard: refuse to rotate the token of an agent_id
	// that's still actively talking to observer, unless caller explicitly
	// opts in. Stops the double-driver-same-id mutual-eviction loop where
	// a stray second process silently knocks the first off observer's auth.
	// Fixes §1.3 #11 of docs/review-2026-06-13.md.
	if !req.Force {
		lastActive, hasLastActive, err := h.s.AgentLastActiveAt(req.WorkspaceID, req.AgentID)
		if err != nil {
			log.Printf("observer: AgentLastActiveAt error: %v", err)
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		if hasLastActive && time.Since(lastActive) < recentRegisterWindow {
			http.Error(w,
				fmt.Sprintf("agent %s already registered recently (last registered %s ago); pass {\"force\":true} to take over",
					req.AgentID, time.Since(lastActive).Round(time.Second)),
				http.StatusConflict)
			return
		}
	}

	token, err := mintAgentToken()
	if err != nil {
		log.Printf("observer: mintAgentToken error: %v", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	// Lazy upsert workspace + upsert agent. Not wrapped in an explicit
	// transaction: each call is single-statement-atomic, and the early
	// rebind check above means we don't create an orphan workspace on
	// the 409 path. Crash between the two would leave an empty workspace
	// visible only to ListWorkspaceSummaries, which is acceptable.
	if err := h.s.UpsertWorkspaceLazy(req.WorkspaceID, req.WorkspaceName, keyID); err != nil {
		log.Printf("observer: UpsertWorkspaceLazy error ws=%s: %v", req.WorkspaceID, err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	agent := observerstore.Agent{
		WorkspaceID: req.WorkspaceID,
		ID:          req.AgentID,
		Role:        req.Role,
		DisplayName: displayName,
	}
	if err := h.s.UpsertAgent(agent, token, keyID); err != nil {
		log.Printf("observer: UpsertAgent error ws=%s id=%s: %v", req.WorkspaceID, req.AgentID, err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	log.Printf("observer: registered agent ws=%s id=%s role=%s via api_key_id=%s",
		req.WorkspaceID, req.AgentID, req.Role, keyID)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(registerResponse{
		WorkspaceID: req.WorkspaceID,
		AgentID:     agent.ID,
		Role:        agent.Role,
		DisplayName: agent.DisplayName,
		Token:       token,
	}); err != nil {
		log.Printf("observer: encode register response error: %v", err)
	}
}

// guardWebToken activates only when OBSERVER_WEB_TOKEN env var is non-empty.
// When active, the request must carry the token via X-Observer-Web-Token
// header or ?web_token= query param. When the env var is empty (default
// local single-user case), requests pass through unchecked.
func (h *handler) guardWebToken(next http.HandlerFunc) http.HandlerFunc {
	want := os.Getenv("OBSERVER_WEB_TOKEN")
	if want == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Observer-Web-Token")
		if got == "" {
			got = r.URL.Query().Get("web_token")
		}
		if got != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (h *handler) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sums, err := h.s.ListWorkspaceSummaries()
	if err != nil {
		log.Printf("observer: ListWorkspaceSummaries error: %v", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if sums == nil {
		sums = []observerstore.WorkspaceSummary{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(sums); err != nil {
		log.Printf("observer: encode listWorkspaces error: %v", err)
	}
}

func validRegisterRole(role string) bool {
	switch role {
	case observer.RoleDriver, observer.RoleMaster, observer.RoleSlave:
		return true
	default:
		return false
	}
}

func mintAgentToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
