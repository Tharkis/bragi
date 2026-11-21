package integrations

import "testing"

func TestSlskdJobState(t *testing.T) {
	f := func(state string, size, done int64) slskdTransfer {
		return slskdTransfer{Filename: `a\b\x.mp3`, State: state, Size: size, BytesTransfered: done}
	}
	for _, tc := range []struct {
		files []slskdTransfer
		want  string
	}{
		{[]slskdTransfer{f("Queued, Remotely", 10, 0)}, JobQueued},
		{[]slskdTransfer{f("Completed, Succeeded", 10, 10), f("InProgress", 10, 3)}, JobDownloading},
		{[]slskdTransfer{f("Completed, Succeeded", 10, 10), f("Completed, Succeeded", 10, 10)}, JobCompleted},
		{[]slskdTransfer{f("Completed, Succeeded", 10, 10), f("Completed, Rejected", 10, 0)}, JobFailed},
		{[]slskdTransfer{f("Completed, Errored", 10, 2), f("Queued, Remotely", 10, 0)}, JobDownloading},
	} {
		if got := slskdJob("j", tc.files); got.State != tc.want {
			t.Errorf("%v: %s, want %s", tc.files, got.State, tc.want)
		}
	}
}

func TestGroupResponses(t *testing.T) {
	rs := []slskdResponse{{Username: "u", HasFreeUploadSlot: true, Files: []slskdFile{
		{Filename: `@@m\Author\Book\02.mp3`, Size: 5}, {Filename: `@@m\Author\Book\01.mp3`, Size: 5},
		{Filename: `@@m\Author\Book\cover.jpg`, Size: 1}, {Filename: `@@m\Author\Book.epub`, Size: 2},
	}}}
	audio := groupResponses(rs, "audiobook")
	if len(audio) != 1 || audio[0].Title != "Author / Book [MP3]" || audio[0].SizeBytes != 11 {
		t.Fatalf("audiobook releases: %+v", audio)
	}
	p, err := decodePick(audio[0].DownloadURL)
	if err != nil || p.User != "u" || len(p.Files) != 3 || p.Files[0].Filename != `@@m\Author\Book\01.mp3` {
		t.Fatalf("pick: %+v %v", p, err)
	}
	books := groupResponses(rs, "book")
	if len(books) != 1 || books[0].Title != "Author / Book.epub" || books[0].Protocol != Soulseek {
		t.Fatalf("book releases: %+v", books)
	}
	user, dir, ok := parseJob(jobName("u", remoteDir(p.Files[0].Filename)))
	if !ok || user != "u" || dir != `@@m\Author\Book` {
		t.Fatalf("job name round trip: %s %s", user, dir)
	}
}
