#!/bin/bash

set -euo pipefail

export REPO="registry-emea.app.corpintra.net/caas-ci"
export TAG="v$(date +"%Y%m%d")-$(git rev-parse --short HEAD)"

make -C prow build-images

# core Prow images
for COMPONENT in crier deck hook horologium prow-controller-manager sinker status-reconciler tide exporter; do
	docker tag "ko.local/${COMPONENT}:${TAG}" "${REPO}/${COMPONENT}:${TAG}"
	docker push "${REPO}/${COMPONENT}:${TAG}"
done

# decoration images
for COMPONENT in clonerefs initupload entrypoint sidecar; do
	docker tag "ko.local/${COMPONENT}:${TAG}" "${REPO}/${COMPONENT}:${TAG}"
	docker push "${REPO}/${COMPONENT}:${TAG}"
done

# external plugins
for COMPONENT in lenses mention cherrypicker; do
	docker tag "ko.local/${COMPONENT}:${TAG}" "${REPO}/${COMPONENT}:${TAG}"
	docker push "${REPO}/${COMPONENT}:${TAG}"
done

# commenter
docker tag "ko.local/commenter:${TAG}" "${REPO}/commenter:${TAG}"
docker push "${REPO}/commenter:${TAG}"

# label sync
docker tag "ko.local/label_sync:${TAG}" "${REPO}/label_sync:${TAG}"
docker push "${REPO}/label_sync:${TAG}"

# ghproxy
docker tag "ko.local/ghproxy:${TAG}" "${REPO}/ghproxy:${TAG}"
docker push "${REPO}/ghproxy:${TAG}"
