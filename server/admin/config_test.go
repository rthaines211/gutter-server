package admin

import "testing"

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "comicvine_api_key is redacted",
			input: "comicvine_api_key: abc123secret\n",
			want:  "comicvine_api_key: «redacted»\n",
		},
		{
			name:  "port is not redacted",
			input: "port: 8080\n",
			want:  "port: 8080\n",
		},
		{
			name:  "generic _key suffix is redacted",
			input: "some_api_key: hunter2\n",
			want:  "some_api_key: «redacted»\n",
		},
		{
			name:  "_token suffix is redacted",
			input: "auth_token: tok_abc\n",
			want:  "auth_token: «redacted»\n",
		},
		{
			name:  "password is redacted",
			input: "password: s3cr3t\n",
			want:  "password: «redacted»\n",
		},
		{
			name:  "indented key is redacted",
			input: "  comicvine_api_key: abc\n",
			want:  "  comicvine_api_key: «redacted»\n",
		},
		{
			name:  "push register_secret is redacted, other push fields are not",
			input: "push:\n  key_path: AuthKey.p8\n  key_id: KID\n  register_secret: abc\n",
			want:  "push:\n  key_path: AuthKey.p8\n  key_id: KID\n  register_secret: «redacted»\n",
		},
		{
			name:  "metron_pass is redacted",
			input: "metron_pass: hunter2\n",
			want:  "metron_pass: «redacted»\n",
		},
		{
			name:  "mixed yaml: only secrets redacted",
			input: "port: 8080\ncomicvine_api_key: abc123\ndb_path: comic.db\n",
			want:  "port: 8080\ncomicvine_api_key: «redacted»\ndb_path: comic.db\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactSecrets(tc.input)
			if got != tc.want {
				t.Errorf("redactSecrets(%q)\n got  %q\n want %q", tc.input, got, tc.want)
			}
		})
	}
}
