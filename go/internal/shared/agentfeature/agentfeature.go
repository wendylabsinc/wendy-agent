// Package agentfeature names entries the agent advertises in
// GetAgentVersionResponse.featureset, so clients gate on a shared constant
// rather than a string literal.
package agentfeature

// ChunkDeployEnv marks an agent whose layer/chunk deploy path (RunContainer)
// applies RunContainerLayersRequest.env. Clients with env to deliver fall back
// to the registry-push create path when it is absent.
const ChunkDeployEnv = "chunk-deploy-env"
