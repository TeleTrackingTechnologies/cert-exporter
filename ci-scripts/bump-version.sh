
#!/usr/bin/env bash

# Bump versions using semversioner

set -ex

previous_version=$(semversioner current-version)

semversioner release

new_version=$(semversioner current-version)

echo "Generating CHANGELOG.md file..."
semversioner changelog > CHANGELOG.md
