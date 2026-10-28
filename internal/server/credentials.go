package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const credentialStoreVersion = 1

type credentialRecord struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Role       string     `json:"role"`
	SecretHash string     `json:"secret_hash"`
	DriverIDs  []string   `json:"driver_ids,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	Revoked    bool       `json:"revoked"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type credentialLibrary struct {
	Version     int                         `json:"version"`
	Credentials map[string]credentialRecord `json:"credentials"`
}

type credentialView struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Role      string     `json:"role"`
	DriverIDs []string   `json:"driver_ids,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	Revoked   bool       `json:"revoked"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type principal struct {
	ID        string
	Name      string
	Role      string
	DriverIDs []string
}

type principalContextKey struct{}

func (c credentialRecord) view() credentialView {
	var revokedAt *time.Time
	if c.RevokedAt != nil {
		timestamp := *c.RevokedAt
		revokedAt = &timestamp
	}
	return credentialView{ID: c.ID, Name: c.Name, Role: c.Role, DriverIDs: append([]string(nil), c.DriverIDs...),
		CreatedAt: c.CreatedAt, Revoked: c.Revoked, RevokedAt: revokedAt}
}

func (c credentialRecord) principal() principal {
	return principal{ID: c.ID, Name: c.Name, Role: c.Role, DriverIDs: append([]string(nil), c.DriverIDs...)}
}

func (s *Server) loadCredentials(bootstrapSecret string) error {
	path := filepath.Join(s.dir, "credentials.json")
	data, err := os.ReadFile(path)
	if err == nil {
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		var library credentialLibrary
		if err := decoder.Decode(&library); err != nil {
			return err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return errors.New("invalid trailing data in credential library")
		}
		if !validCredentialLibrary(library) {
			return errors.New("invalid credential library")
		}
		s.credentials = library.Credentials
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// A missing library is the explicit migration boundary for existing installs:
	// seed their current EMBERDECK_API_KEY as a durable, hashed admin credential.
	id, err := randomID()
	if err != nil {
		return err
	}
	credential := credentialRecord{ID: id, Name: "bootstrap-admin", Role: "admin", SecretHash: hashSecret(bootstrapSecret), CreatedAt: time.Now().UTC()}
	s.credentials = map[string]credentialRecord{id: credential}
	return s.persistCredentials(s.credentials)
}

func validCredentialLibrary(library credentialLibrary) bool {
	if library.Version != credentialStoreVersion || library.Credentials == nil || len(library.Credentials) == 0 {
		return false
	}
	claimedDrivers := make(map[string]string)
	seenHashes := make(map[string]bool)
	for id, credential := range library.Credentials {
		if !validHexID(id) || credential.ID != id || !validCredentialName(credential.Name) || !validRole(credential.Role) ||
			!validSecretHash(credential.SecretHash) || credential.CreatedAt.IsZero() {
			return false
		}
		if seenHashes[credential.SecretHash] {
			return false
		}
		seenHashes[credential.SecretHash] = true
		if credential.Revoked != (credential.RevokedAt != nil) {
			return false
		}
		if credential.Role != "worker" {
			if len(credential.DriverIDs) != 0 {
				return false
			}
			continue
		}
		if len(credential.DriverIDs) == 0 {
			return false
		}
		seen := make(map[string]bool, len(credential.DriverIDs))
		for _, driverID := range credential.DriverIDs {
			if !driverName.MatchString(driverID) || seen[driverID] {
				return false
			}
			seen[driverID] = true
			if !credential.Revoked {
				if _, exists := claimedDrivers[driverID]; exists {
					return false
				}
				claimedDrivers[driverID] = id
			}
		}
	}
	return true
}

func validCredentialName(name string) bool {
	return name == strings.TrimSpace(name) && name != "" && len(name) <= 120
}

func validRole(role string) bool {
	switch role {
	case "viewer", "operator", "admin", "worker":
		return true
	default:
		return false
	}
}

func validSecretHash(hash string) bool {
	if len(hash) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(hash)
	return err == nil && hex.EncodeToString(decoded) == hash
}

func hashSecret(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(digest[:])
}

func (s *Server) persistCredentials(credentials map[string]credentialRecord) error {
	if s.closed {
		return errors.New("server is closed")
	}
	if s.storageErr != nil {
		return s.storageErr
	}
	library := credentialLibrary{Version: credentialStoreVersion, Credentials: credentials}
	published, err := durableJSON(filepath.Join(s.dir, "credentials.json"), library)
	if published && err != nil {
		s.failStorage(err)
	}
	if err != nil {
		return err
	}
	s.credentials = credentials
	return nil
}

func (s *Server) identify(secret string) (credentialRecord, bool) {
	if secret == "" {
		return credentialRecord{}, false
	}
	provided := sha256.Sum256([]byte(secret))
	var found credentialRecord
	matched := 0
	for _, credential := range s.credentials {
		stored, err := hex.DecodeString(credential.SecretHash)
		if err != nil {
			continue
		}
		match := subtle.ConstantTimeCompare(provided[:], stored)
		active := subtle.ConstantTimeByteEq(boolByte(!credential.Revoked), 1)
		if match&active == 1 {
			found = credential
			matched = 1
		}
	}
	return found, matched == 1
}

func boolByte(value bool) byte {
	if value {
		return 1
	}
	return 0
}

func bearerSecret(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) || len(header) == len(prefix) {
		return "", false
	}
	return header[len(prefix):], true
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	secret, valid := bearerSecret(r.Header.Get("Authorization"))
	if !valid {
		fail(w, http.StatusUnauthorized, "authentication required")
		return r, false
	}
	s.mu.Lock()
	credential, ok := s.identify(secret)
	s.mu.Unlock()
	if !ok {
		fail(w, http.StatusUnauthorized, "authentication required")
		return r, false
	}
	return withPrincipal(r, credential.principal()), true
}

func withPrincipal(r *http.Request, p principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), principalContextKey{}, p))
}

func requestPrincipal(r *http.Request) principal {
	p, _ := r.Context().Value(principalContextKey{}).(principal)
	return p
}

func containsDriver(driverIDs []string, id string) bool {
	for _, driverID := range driverIDs {
		if driverID == id {
			return true
		}
	}
	return false
}

func (s *Server) handleAPI(pattern string, allowedRoles []string, workerScope bool, handler http.HandlerFunc) {
	allowed := make(map[string]bool, len(allowedRoles))
	for _, role := range allowedRoles {
		allowed[role] = true
	}
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		identity := requestPrincipal(r)
		if !allowed[identity.Role] {
			fail(w, http.StatusForbidden, "credential role is not authorized for this endpoint")
			return
		}
		if workerScope && identity.Role == "worker" && !containsDriver(identity.DriverIDs, r.PathValue("id")) {
			fail(w, http.StatusForbidden, "worker credential is not authorized for this driver ID")
			return
		}
		handler(w, r)
	})
}

func (s *Server) authInfo(w http.ResponseWriter, r *http.Request) {
	identity := requestPrincipal(r)
	respond(w, http.StatusOK, map[string]any{"id": identity.ID, "name": identity.Name, "role": identity.Role, "driver_ids": identity.DriverIDs})
}

func (s *Server) listCredentials(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.credentials))
	for id := range s.credentials {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]credentialView, 0, len(ids))
	for _, id := range ids {
		result = append(result, s.credentials[id].view())
	}
	respond(w, http.StatusOK, result)
}

func (s *Server) createCredential(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name      string   `json:"name"`
		Role      string   `json:"role"`
		DriverIDs []string `json:"driver_ids,omitempty"`
	}
	if !decodeBounded(w, r, &input, 64<<10) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validCredentialName(input.Name) || !validRole(input.Role) {
		fail(w, http.StatusBadRequest, "credential name or role is invalid")
		return
	}
	if input.Role == "worker" {
		if len(input.DriverIDs) == 0 {
			fail(w, http.StatusBadRequest, "worker credentials require one or more driver_ids")
			return
		}
		seen := make(map[string]bool, len(input.DriverIDs))
		for _, driverID := range input.DriverIDs {
			if !driverName.MatchString(driverID) || seen[driverID] {
				fail(w, http.StatusBadRequest, "worker driver_ids must be unique valid driver IDs")
				return
			}
			seen[driverID] = true
		}
		sort.Strings(input.DriverIDs)
	} else if len(input.DriverIDs) != 0 {
		fail(w, http.StatusBadRequest, "only worker credentials can have driver_ids")
		return
	}
	id, err := randomID()
	if err != nil {
		fail(w, http.StatusInternalServerError, "could not allocate credential ID")
		return
	}
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		fail(w, http.StatusInternalServerError, "could not allocate credential secret")
		return
	}
	secret := base64.RawURLEncoding.EncodeToString(entropy[:])
	credential := credentialRecord{ID: id, Name: input.Name, Role: input.Role, SecretHash: hashSecret(secret), DriverIDs: append([]string(nil), input.DriverIDs...), CreatedAt: time.Now().UTC()}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.credentials {
		if input.Role == "worker" && existing.Role == "worker" && !existing.Revoked {
			for _, driverID := range input.DriverIDs {
				if containsDriver(existing.DriverIDs, driverID) {
					fail(w, http.StatusConflict, "driver ID is already assigned to an active worker credential")
					return
				}
			}
		}
	}
	credentials := copyCredentials(s.credentials)
	credentials[id] = credential
	if err := s.persistCredentials(credentials); err != nil {
		fail(w, http.StatusInternalServerError, "could not persist credential")
		return
	}
	respond(w, http.StatusCreated, map[string]any{"id": credential.ID, "name": credential.Name, "role": credential.Role,
		"driver_ids": credential.DriverIDs, "created_at": credential.CreatedAt, "key": secret})
}

func (s *Server) revokeCredential(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	credential, ok := s.credentials[id]
	if !ok || credential.Revoked {
		fail(w, http.StatusNotFound, "credential not found")
		return
	}
	if credential.Role == "admin" {
		activeAdmins := 0
		for _, current := range s.credentials {
			if current.Role == "admin" && !current.Revoked {
				activeAdmins++
			}
		}
		if activeAdmins <= 1 {
			fail(w, http.StatusConflict, "create another active admin credential before revoking the last admin")
			return
		}
	}
	credential.Revoked = true
	revokedAt := time.Now().UTC()
	credential.RevokedAt = &revokedAt
	credentials := copyCredentials(s.credentials)
	credentials[id] = credential
	if err := s.persistCredentials(credentials); err != nil {
		fail(w, http.StatusInternalServerError, "could not persist credential revocation")
		return
	}

	jobs := s.copyJobs()
	changed := false
	for _, driverID := range credential.DriverIDs {
		driver, online := s.drivers[driverID]
		if online && driver.CredentialID == id {
			delete(s.drivers, driverID)
		}
		for jobID, job := range jobs {
			if job.DriverID == driverID && activeJob(job.State) && online && driver.CredentialID == id {
				job.State = "interrupted"
				job.Error = "worker credential revoked; inspect machine before creating a new job"
				job.Lease = ""
				job.Execution = markExecutionUnknown(job.Execution)
				jobs[jobID] = job
				changed = true
			}
		}
	}
	if changed {
		if err := s.save(jobs); err != nil {
			// The credential revocation is already durable. Until the active job
			// transition is certain, disable all APIs; restart recovery interrupts it.
			s.failStorage(err)
			fail(w, http.StatusInternalServerError, "could not persist interrupted job after credential revocation")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func copyCredentials(credentials map[string]credentialRecord) map[string]credentialRecord {
	copy := make(map[string]credentialRecord, len(credentials))
	for id, credential := range credentials {
		credential.DriverIDs = append([]string(nil), credential.DriverIDs...)
		copy[id] = credential
	}
	return copy
}
