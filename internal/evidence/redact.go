/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package evidence

import (
	"regexp"
)

// redactors remove material that must not leave the cluster. Container logs routinely carry
// credentials, and evidence is forwarded to an external analyzer, so scrubbing is a requirement
// rather than a refinement.
//
// Each pattern preserves enough surrounding text for the line to stay diagnostically useful: a log
// line reading "FATAL: password authentication failed for user app" must remain recognisable after
// any embedded credential is removed.
var redactors = []struct {
	name    string
	pattern *regexp.Regexp
	replace string
}{
	{
		name:    "assignment",
		pattern: regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential)s?\s*[=:]\s*"?[^\s"',;)]{4,}"?`),
		replace: `${1}=[REDACTED]`,
	},
	{
		name:    "bearer",
		pattern: regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`),
		replace: "Bearer [REDACTED]",
	},
	{
		name:    "jwt",
		pattern: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`),
		replace: "[REDACTED_JWT]",
	},
	{
		name:    "connection string",
		pattern: regexp.MustCompile(`\b([a-zA-Z][a-zA-Z0-9+.-]*://)([^\s:@/]+):([^\s@/]+)@`),
		replace: `${1}${2}:[REDACTED]@`,
	},
	{
		name:    "aws access key",
		pattern: regexp.MustCompile(`\b(A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z0-9]{16}\b`),
		replace: "[REDACTED_AWS_KEY]",
	},
	{
		name:    "kubernetes service account token",
		pattern: regexp.MustCompile(`\bey[A-Za-z0-9_-]{20,}`),
		replace: "[REDACTED_TOKEN]",
	},
}

// Redact removes sensitive material from gathered content, returning the scrubbed text and how many
// substitutions were made. A non zero count is recorded on the diagnosis so that an operator can see
// that secrets were present and were not forwarded.
func Redact(content string) (string, int32) {
	var count int32
	for _, r := range redactors {
		matches := r.pattern.FindAllStringIndex(content, -1)
		if len(matches) == 0 {
			continue
		}
		count += int32(len(matches))
		content = r.pattern.ReplaceAllString(content, r.replace)
	}
	return content, count
}

// Truncate limits content to the schema's per item ceiling, keeping the tail. For logs the most
// recent output is the most diagnostic, since it contains the error that preceded termination.
func Truncate(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	const marker = "[...truncated, showing the most recent output...]\n"
	keep := limit - len(marker)
	if keep <= 0 {
		return content[:limit]
	}
	return marker + content[len(content)-keep:]
}
