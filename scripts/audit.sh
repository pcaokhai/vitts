#!/usr/bin/env bash
# Dependency vulnerability scan with reviewed, expiring exceptions.
#
# govulncheck has no ignore mechanism, and a gate that cannot express "we looked at this
# and it does not apply" is a gate people route around. This wraps it: anything not
# listed in security/accepted.yaml fails, and so does any acceptance past its review
# date.
set -euo pipefail
cd "$(dirname "$0")/.."

ACCEPTED_FILE=security/accepted.yaml

# Audit with the toolchain the image ships, not whatever the developer has installed.
# govulncheck reports standard-library advisories against the *building* toolchain, so a
# laptop one patch release behind produces findings that do not exist in the artefact -
# and, worse, a laptop ahead of the image would hide ones that do. Go fetches this
# automatically. Keep it in step with the FROM line in gateway/Dockerfile.
export GOTOOLCHAIN=go1.26.8

echo "==> govulncheck (gateway)"
( cd gateway && CGO_ENABLED=0 govulncheck -json ./... ) > /tmp/vitts-govulncheck.json

python3 - "$ACCEPTED_FILE" /tmp/vitts-govulncheck.json <<'PY'
import datetime, json, re, sys

accepted_path, findings_path = sys.argv[1], sys.argv[2]

# A three-field subset of YAML rather than a dependency: the file is ours and its shape
# is fixed, and a scanner that needs a package installed to run is one more thing that
# can fail in CI.
accepted, current = {}, None
for line in open(accepted_path, encoding="utf-8"):
    stripped = line.strip()
    if stripped.startswith("- id:"):
        current = stripped.split(":", 1)[1].strip()
        accepted[current] = {}
    elif current and stripped.startswith("review_by:"):
        accepted[current]["review_by"] = stripped.split(":", 1)[1].strip()

# govulncheck -json emits a stream of objects; the ones that matter carry an OSV id and
# a finding that is actually reached.
found = set()
for match in re.finditer(r'"osv":\s*"(GO-\d{4}-\d+)"', open(findings_path, encoding="utf-8").read()):
    found.add(match.group(1))

today = datetime.date.today()
failures = []

for osv in sorted(found):
    if osv not in accepted:
        failures.append(f"{osv}: not reviewed — assess it and add it to {accepted_path}, or upgrade")
        continue
    review_by = accepted[osv].get("review_by")
    if not review_by:
        failures.append(f"{osv}: accepted without a review_by date")
        continue
    if datetime.date.fromisoformat(review_by) < today:
        failures.append(f"{osv}: acceptance expired on {review_by} — review it again")

for osv in sorted(set(accepted) - found):
    print(f"  note: {osv} is accepted but no longer reported; remove it from {accepted_path}")

if failures:
    print("\nvulnerability gate failed:")
    for failure in failures:
        print(f"  {failure}")
    sys.exit(1)

print(f"  {len(found)} finding(s), all reviewed and unexpired")
PY

echo "==> pip-audit (worker)"
( cd worker && uv export --frozen --no-dev --no-emit-project > /tmp/vitts-requirements.txt )
uvx pip-audit --strict --requirement /tmp/vitts-requirements.txt
