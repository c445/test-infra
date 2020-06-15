
# Deck

## Debugging via Intellij / VSCode

This section describes how to debug Deck locally by running it inside 
VSCode or Intellij.

```bash
TEST_INFRA_DIR=${GOPATH}/src/k8s.io/test-infra

# Prepare assets
cd ${TEST_INFRA_DIR}
bazel build //prow/external-plugins/lenses:image.tar
mkdir -p /tmp/lenses
tar -xvf ./bazel-bin/prow/external-plugins/lenses/lenses-layer.tar -C /tmp/lenses


# Start Deck via go or in your IDE with the following arguments:
--config-path=/home/sbuerin/code/src/git.daimler.com/c445/t1/prow/config/prow/config.yaml
--job-config-path=/home/sbuerin/code/src/git.daimler.com/c445/t1/prow/config/jobs
--s3-credentials-file=/tmp/s3.json
--spyglass-files-location=/tmp/deck/lenses
```
