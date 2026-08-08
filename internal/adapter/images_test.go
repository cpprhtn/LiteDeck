package adapter

import "testing"

func TestParseImagesGolden(t *testing.T) {
	imgs, err := ParseImages(loadGolden(t, "docker", "images.jsonl"))
	if err != nil {
		t.Fatalf("ParseImages: %v", err)
	}
	if len(imgs) != 2 {
		t.Fatalf("got %d images, want 2", len(imgs))
	}

	byRepo := map[string]Image{}
	for _, i := range imgs {
		byRepo[i.Repository] = i
	}

	alpine := byRepo["alpine"]
	if alpine.Tag != "3.20" {
		t.Errorf("alpine tag = %q", alpine.Tag)
	}
	if alpine.Dangling {
		t.Error("a tagged image was marked dangling")
	}
	// docker uses decimal units, so 8.82MB is 8.82e6 bytes, not 8.82 MiB.
	if alpine.SizeBytes < 8_000_000 || alpine.SizeBytes > 9_500_000 {
		t.Errorf("alpine size = %d bytes (from %q)", alpine.SizeBytes, alpine.Size)
	}
	// The count of containers using an image is what makes it safe to reclaim.
	if alpine.Containers != 1 {
		t.Errorf("alpine Containers = %d, want 1", alpine.Containers)
	}
	if byRepo["busybox"].Containers != 0 {
		t.Errorf("busybox Containers = %d, want 0", byRepo["busybox"].Containers)
	}

	// Largest first: the reason to open this list is disk space.
	for i := 1; i < len(imgs); i++ {
		if imgs[i-1].SizeBytes < imgs[i].SizeBytes {
			t.Errorf("not sorted by size: %d then %d", imgs[i-1].SizeBytes, imgs[i].SizeBytes)
		}
	}
}

// TestParseImagesDangling: an untagged layer left by a rebuild is the usual
// answer to "where did my disk go", so it has to be identifiable.
func TestParseImagesDangling(t *testing.T) {
	const in = `{"ID":"sha256:aaa","Repository":"<none>","Tag":"<none>","Size":"1.2GB","Containers":"0","CreatedAt":"2026-01-01 00:00:00 +0000 UTC"}
{"ID":"sha256:bbb","Repository":"myapp","Tag":"latest","Size":"250MB","Containers":"N/A","CreatedAt":"2026-01-01 00:00:00 +0000 UTC"}`
	imgs, err := ParseImages([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 2 {
		t.Fatalf("got %d", len(imgs))
	}
	if !imgs[0].Dangling {
		t.Errorf("<none>:<none> not marked dangling: %+v", imgs[0])
	}
	if imgs[0].SizeBytes != 1_200_000_000 {
		t.Errorf("1.2GB = %d bytes", imgs[0].SizeBytes)
	}
	// "N/A" means the daemon did not compute it — reporting 0 would say
	// "safe to delete" about an image that might be in use.
	if imgs[1].Containers != -1 {
		t.Errorf("Containers for N/A = %d, want -1", imgs[1].Containers)
	}
}

func TestParseDockerSize(t *testing.T) {
	cases := map[string]int64{
		"4.17MB":  4_170_000,
		"8.82MB":  8_820_000,
		"1.2GB":   1_200_000_000,
		"500kB":   500_000,
		"120B":    120,
		"N/A":     0,
		"":        0,
		"garbage": 0,
	}
	for in, want := range cases {
		if got := parseDockerSize(in); got != want {
			t.Errorf("parseDockerSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseVolumesGolden(t *testing.T) {
	vols, err := ParseVolumes(
		loadGolden(t, "docker", "volumes.jsonl"),
		loadGolden(t, "docker", "volumes-dangling.txt"),
	)
	if err != nil {
		t.Fatalf("ParseVolumes: %v", err)
	}
	if len(vols) != 2 {
		t.Fatalf("got %d volumes, want 2", len(vols))
	}

	byName := map[string]Volume{}
	for _, v := range vols {
		byName[v.Name] = v
	}
	// ld-data is mounted by a running container; ld-cache is not.
	if !byName["ld-data"].InUse {
		t.Error("a mounted volume was reported as unused")
	}
	if byName["ld-cache"].InUse {
		t.Error("an unreferenced volume was reported as in use")
	}
	if byName["ld-data"].Mountpoint == "" || byName["ld-data"].Driver != "local" {
		t.Errorf("ld-data = %+v", byName["ld-data"])
	}

	// Unused first: those are the ones the user can act on.
	if vols[0].InUse {
		t.Errorf("in-use volume sorted ahead of an unused one: %+v", vols)
	}
}

func TestImageVolumeParsersReturnArrays(t *testing.T) {
	imgs, err := ParseImages(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertArray(t, "ParseImages(empty)", imgs)

	vols, err := ParseVolumes(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertArray(t, "ParseVolumes(empty)", vols)
}
