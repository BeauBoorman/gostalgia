package sdk

import "testing"

func TestManifestContract(t *testing.T) {
	good := `{"id":"com.test.app","name":"App","version":"1.2.3","entrypoint":"app","permissions":["ipc","fs.read"]}`
	if _, err := ParseManifest([]byte(good)); err != nil {
		t.Fatal(err)
	}
	goodExt := `{"id":"com.test.ext","name":"External App","version":"1.0.0","mode":"external","executable":"/bin/ext","args":["--flag"],"protocol_version":1,"permissions":["ipc"]}`
	if _, err := ParseManifest([]byte(goodExt)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{}`, `null`, good + ` {}`, good + ` garbage`,
		`{"id":"com.test.app","name":"App","version":"1","entrypoint":"app"}`,
		`{"id":"com.test.app","name":"App","version":"1.0.0","entrypoint":"app","permissions":["admin"]}`,
		`{"id":"com.test.app","name":"App","version":"1.0.0","entrypoint":"app","permissions":["ipc","ipc"]}`,
		`{"id":"com.test.app","name":"App","version":"1.0.0","entrypoint":"app","permissions":["typo"]}`,
		`{"id":"com.test.app","name":"App","version":"1.0.0","entrypoint":"app","unknown":true}`,
		`{"id":"com.test.app","name":"App","version":"1.0.0","mode":"invalid","executable":"foo"}`,
		`{"id":"com.test.app","name":"App","version":"1.0.0","mode":"inproc","executable":"foo","entrypoint":"bar"}`,
		`{"id":"com.test.app","name":"App","version":"1.0.0","mode":"external","executable":""}`,
		`{"id":"com.test.app","name":"App","version":"1.0.0","mode":"external","executable":"foo","protocol_version":999}`,
	} {
		if _, err := ParseManifest([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
