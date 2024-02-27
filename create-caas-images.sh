#!/bin/bash

set -euo pipefail

export REGISTRY="registry-emea.app.corpintra.net/caas-ci"
export TAG="v$(date +"%Y%m%d")-$(git rev-parse --short HEAD)"

make -C prow build-images
PROW_IMAGE="prow/external-plugins/lenses" make -C prow build-single-image
PROW_IMAGE="prow/external-plugins/mention" make -C prow build-single-image

# core Prow images
for COMPONENT in crier deck hook horologium prow-controller-manager sinker status-reconciler tide exporter; do
	docker tag "ko.local/${COMPONENT}:${TAG}" "${REGISTRY}/${COMPONENT}:${TAG}"
	docker push "${REGISTRY}/${COMPONENT}:${TAG}"
done

# decoration images
for COMPONENT in clonerefs initupload entrypoint sidecar; do
	docker tag "ko.local/${COMPONENT}:${TAG}" "${REGISTRY}/${COMPONENT}:${TAG}"
	docker push "${REGISTRY}/${COMPONENT}:${TAG}"
done

# external plugins
for COMPONENT in lenses mention cherrypicker; do
	docker tag "ko.local/${COMPONENT}:${TAG}" "${REGISTRY}/${COMPONENT}:${TAG}"
	docker push "${REGISTRY}/${COMPONENT}:${TAG}"
done

# commenter
docker tag "ko.local/commenter:${TAG}" "${REGISTRY}/commenter:${TAG}"
docker push "${REGISTRY}/commenter:${TAG}"

# label sync
docker tag "ko.local/label_sync:${TAG}" "${REGISTRY}/label_sync:${TAG}"
docker push "${REGISTRY}/label_sync:${TAG}"

# ghproxy
docker tag "ko.local/ghproxy:${TAG}" "${REGISTRY}/ghproxy:${TAG}"
docker push "${REGISTRY}/ghproxy:${TAG}"
