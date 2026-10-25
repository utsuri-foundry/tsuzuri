package tsuzuri

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// OperationOptions supplies trusted policy for one call without changing process globals.
// Environment replaces the child environment; nil inherits the process environment.
// An explicit environment requires an absolute GitExecutable. Keep the slice
// unchanged until return. Pass credentials through Git config environment variables,
// never command arguments; see docs/integration.md for supervisor setup.
// Nonempty Expected fields must match: mismatches return *Error with Kind/Code
// "validation" and ExitCode 3; malformed options return "usage" (exit 2).
type OperationOptions struct {
	GitExecutable          string
	Environment            []string
	ExpectedRemoteURL      string
	ExpectedRepositoryID   string
	ExpectedClaimNamespace string
}

func operationInputs(options []OperationOptions) (OperationOptions, []string, string, error) {
	if len(options) > 1 {
		return OperationOptions{}, nil, "", failure("usage", "at most one operation policy is allowed")
	}
	var policy OperationOptions
	if len(options) == 1 {
		policy = options[0]
	}
	path := policy.GitExecutable
	if policy.Environment != nil && !filepath.IsAbs(path) {
		return policy, nil, "", failure("usage", "an explicit environment requires an absolute Git executable")
	}
	if path == "" {
		var err error
		path, err = exec.LookPath("git")
		if err != nil {
			return policy, nil, "", invalid("Git executable unavailable").causedBy(err)
		}
	} else if !filepath.IsAbs(path) {
		return policy, nil, "", failure("usage", "Git executable must be absolute")
	}
	environment := policy.Environment
	if environment == nil {
		environment = os.Environ()
	}
	clean := make([]string, 0, len(environment))
	seen := map[string]bool{}
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsRune(entry, 0) || seen[key] {
			return policy, nil, "", failure("usage", "invalid or duplicate environment entry")
		}
		seen[key] = true
		// Repository selection must come from the client; tracing can disclose
		// credentials even when normal Git diagnostics are redacted.
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_CURL_VERBOSE":
			continue
		}
		if strings.HasPrefix(key, "GIT_TRACE") {
			continue
		}
		clean = append(clean, entry)
	}
	// The repository retains only this operation's policy, never the caller slice.
	policy.Environment = nil
	return policy, clean, path, nil
}

func (r *repository) checkConfiguration(c Config) error {
	if r.policy.ExpectedRepositoryID != "" && c.RepositoryID != r.policy.ExpectedRepositoryID {
		return invalid("repository differs from operation policy")
	}
	if r.policy.ExpectedClaimNamespace != "" && c.ClaimNamespace != r.policy.ExpectedClaimNamespace {
		return invalid("claim namespace differs from operation policy")
	}
	return nil
}
