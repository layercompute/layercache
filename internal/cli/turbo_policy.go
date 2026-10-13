package cli

import (
	"encoding/json"
	"strings"
)

const turboSyncPolicy = "local:,remote:rw"
const turboOriginalPolicyEnvironment = "LAYER_CACHE_TURBO_CACHE_ORIGINAL"

func withTurboSyncPolicy(environment []string) []string {
	current := environmentValue(environment, "TURBO_CACHE")
	if _, owned := originalTurboPolicy(environment); current != turboSyncPolicy || !owned {
		encoded, _ := json.Marshal(current)
		environment = replaceEnvironment(environment, turboOriginalPolicyEnvironment, string(encoded))
	}
	return replaceEnvironment(environment, "TURBO_CACHE", turboSyncPolicy)
}

// Restore the caller's policy only while our forced policy is still active.
// A nested caller can deliberately select a different policy before bypassing.
func withoutTurboSyncPolicy(environment []string) []string {
	original, owned := originalTurboPolicy(environment)
	if !owned {
		return environment
	}
	if environmentValue(environment, "TURBO_CACHE") == turboSyncPolicy {
		environment = replaceEnvironment(environment, "TURBO_CACHE", original)
	}
	return replaceEnvironment(environment, turboOriginalPolicyEnvironment, "")
}

func originalTurboPolicy(environment []string) (string, bool) {
	var original *string
	if json.Unmarshal([]byte(environmentValue(environment, turboOriginalPolicyEnvironment)), &original) != nil || original == nil {
		return "", false
	}
	return *original, true
}

func environmentValue(environment []string, key string) string {
	for index := len(environment) - 1; index >= 0; index-- {
		if value, found := strings.CutPrefix(environment[index], key+"="); found {
			return value
		}
	}
	return ""
}
