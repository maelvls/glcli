package gitlab

import (
	"encoding/json"
	"testing"
	"time"
)

// The example answer of GET /projects/:id/merge_requests/:iid/versions in
// GitLab's documentation (doc/api/merge_requests.md), with the patch ID
// of the second version made null.
const documentedVersions = `[{
  "id": 110,
  "head_commit_sha": "33e2ee8579fda5bc36accc9c6fbd0b4fefda9e30",
  "base_commit_sha": "eeb57dffe83deb686a60a71c16c32f71046868fd",
  "start_commit_sha": "eeb57dffe83deb686a60a71c16c32f71046868fd",
  "created_at": "2016-07-26T14:44:48.926Z",
  "merge_request_id": 105,
  "state": "collected",
  "real_size": "1",
  "patch_id_sha": "d504412d5b6e6739647e752aff8e468dde093f2f"
}, {
  "id": 108,
  "head_commit_sha": "3eed087b29835c48015768f839d76e5ea8f07a24",
  "base_commit_sha": "eeb57dffe83deb686a60a71c16c32f71046868fd",
  "start_commit_sha": "eeb57dffe83deb686a60a71c16c32f71046868fd",
  "created_at": "2016-07-25T14:21:33.028Z",
  "merge_request_id": 105,
  "state": "collected",
  "real_size": "1",
  "patch_id_sha": null
}]`

func TestVersionPatchID(t *testing.T) {
	var vs []Version
	if err := json.Unmarshal([]byte(documentedVersions), &vs); err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[0].PatchIDSHA != "d504412d5b6e6739647e752aff8e468dde093f2f" || vs[0].HeadCommitSHA != "33e2ee8579fda5bc36accc9c6fbd0b4fefda9e30" {
		t.Errorf("versions = %+v", vs)
	}
	if !vs[0].CreatedAt.Equal(time.Date(2016, 7, 26, 14, 44, 48, 926e6, time.UTC)) {
		t.Errorf("created_at = %v", vs[0].CreatedAt)
	}
	// A version GitLab computed no patch ID for (null): empty, unknown.
	if vs[1].PatchIDSHA != "" {
		t.Errorf("patch ID of a null = %q", vs[1].PatchIDSHA)
	}
}
