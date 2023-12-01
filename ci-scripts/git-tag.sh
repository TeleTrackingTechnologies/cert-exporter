  
#!/bin/bash

# Commit back to the repository
# version number, push the tag back to the remote.

set -ex

# Tag and push
tag=$(semversioner current-version)

git config --local user.email "info@teletracking.com"
git config --local user.name "CI Build"

# Commit
git add .
git commit -m "Update files for new version '${tag}' [skip ci]"

# Tag
git tag -a -m "Tagging for release ${tag}" "${tag}"