package serverhost

import (
	"net/http"
	"testing"

	"github.com/bboxsys/platform/ai"
	"github.com/bboxsys/platform/ai/instance"
	"github.com/bboxsys/platform/logger"
	"github.com/bboxsys/platform/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A one-operation spec, enough for the instance to generate an API tool.
const spec = `{"openapi":"3.0.3","info":{"title":"t","version":"1"},"paths":{"/api/v1/things":{"get":{"operationId":"listThings","summary":"List things","responses":{"200":{"description":"OK"}}}}}}`

func TestNew(t *testing.T) {
	srv := &server.Server{
		ServerOptions: &server.ServerOptions{Component: "things_service"},
		Srv:           &http.Server{Handler: http.NotFoundHandler()},
		Logger:        logger.NewLogger("test", "things"),
	}
	inst, err := New(srv, instance.Options{Dir: t.TempDir(), Credentials: ai.StaticCredentials{}, OpenAPI: []byte(spec)})
	require.NoError(t, err)
	defer inst.Close()
	o := inst.Options()
	assert.Equal(t, "things_service", o.Name)
	assert.Equal(t, "things_service", o.Service)
	assert.NotNil(t, o.Handler)
	assert.Same(t, srv.Logger, o.Logger)
	assert.Nil(t, o.Publisher, "no messenger client, no publisher")
	assert.NotEmpty(t, inst.APITools())

	_, err = New(nil, instance.Options{})
	assert.Error(t, err)
}
