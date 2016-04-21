package commands

import (
	"os"

	"github.com/github/git-lfs/lfs"
	"github.com/github/git-lfs/git"
)

var uploadMissingErr = "%s does not exist in .git/lfs/objects. Tried %s, which matches %s."

type uploadContext struct {
	DryRun       bool
	uploadedOids lfs.StringSet
}

func newUploadContext(dryRun bool) *uploadContext {
	return &uploadContext{
		DryRun:       dryRun,
		uploadedOids: lfs.NewStringSet(),
	}
}

// AddUpload adds the given oid to the set of oids that have been uploaded in
// the current process.
func (c *uploadContext) SetUploaded(oid string) {
	c.uploadedOids.Add(oid)
}

// HasUploaded determines if the given oid has already been uploaded in the
// current process.
func (c *uploadContext) HasUploaded(oid string) bool {
	return c.uploadedOids.Contains(oid)
}

func (c *uploadContext) prepareUpload(unfiltered []*lfs.WrappedPointer) (*lfs.TransferQueue, []*lfs.WrappedPointer) {
	numUnfiltered := len(unfiltered)
	uploadables := make([]*lfs.WrappedPointer, 0, numUnfiltered)
	missingLocalObjects := make([]*lfs.WrappedPointer, 0, numUnfiltered)
	numObjects := 0
	totalSize := int64(0)
	missingSize := int64(0)
	for _, ptr := range unfiltered {git.Logger.Printf("prepareUpload: unfiltered:%+v\n", ptr)}
	defer git.Logger.Printf("prepareUpload ended\n");

	// separate out objects that _should_ be uploaded, but don't exist in
	// .git/lfs/objects. Those will skipped if the server already has them.
	for _, p := range unfiltered {
		// object already uploaded in this process, skip!
		if c.HasUploaded(p.Oid) {
			git.Logger.Printf("prepareUpload: object %+v has already been uploaded\n", p);
			continue
		}

		numObjects += 1
		totalSize += p.Size

		if lfs.ObjectExistsOfSize(p.Oid, p.Size) {
			git.Logger.Printf("prepareUpload: object %+v should be uploaded. Add to uploadables\n", p);
			uploadables = append(uploadables, p)
		} else {
			git.Logger.Printf("prepareUpload: we thin object %+v should be uploaded ... but we don't have it. Add to missingLocalObjects\n", p);
			// We think we need to push this but we don't have it
			// Store for server checking later
			missingLocalObjects = append(missingLocalObjects, p)
			missingSize += p.Size
		}
	}

	// check to see if the server has the missing objects.
	c.checkMissing(missingLocalObjects, missingSize)

	// build the TransferQueue, automatically skipping any missing objects that
	// the server already has.
	uploadQueue := lfs.NewUploadQueue(numObjects, totalSize, c.DryRun)
	for _, p := range missingLocalObjects {
		if c.HasUploaded(p.Oid) {
			uploadQueue.Skip(p.Size)
		} else {
			uploadables = append(uploadables, p)
		}
	}

	return uploadQueue, uploadables
}

// This checks the given slice of pointers that don't exist in .git/lfs/objects
// against the server. Anything the server already has does not need to be
// uploaded again.
func (c *uploadContext) checkMissing(missing []*lfs.WrappedPointer, missingSize int64) {
	for _, m := range missing {git.Logger.Printf("checkMissing: Ask the server for this object missing locally: %+v\n", m)} ;
	numMissing := len(missing)
	if numMissing == 0 {
		return
	}

	checkQueue := lfs.NewDownloadCheckQueue(numMissing, missingSize, true)
	for _, p := range missing {
		checkQueue.Add(lfs.NewDownloadCheckable(p))
	}

	// this channel is filled with oids for which Check() succeeded & Transfer() was called
	transferc := checkQueue.Watch()
	done := make(chan int)
	go func() {
		for oid := range transferc {
			c.SetUploaded(oid)
		}
		done <- 1
	}()

	// Currently this is needed to flush the batch but is not enough to sync transferc completely
	checkQueue.Wait()
	<-done
}

func upload(c *uploadContext, unfiltered []*lfs.WrappedPointer) {
	if c.DryRun {
		for _, p := range unfiltered {
			if c.HasUploaded(p.Oid) {
				continue
			}

			Print("push %s => %s", p.Oid, p.Name)
			c.SetUploaded(p.Oid)
		}

		return
	}

	q, pointers := c.prepareUpload(unfiltered)
	git.Logger.Printf("upload: prepareUpload returned the following objects: %+v\n", pointers);
	for _, p := range pointers {
		u, err := lfs.NewUploadable(p.Oid, p.Name)
		if err != nil {
			if lfs.IsCleanPointerError(err) {
				Exit(uploadMissingErr, p.Oid, p.Name, lfs.ErrorGetContext(err, "pointer").(*lfs.Pointer).Oid)
			} else {
				ExitWithError(err)
			}
		}

		q.Add(u)
		c.SetUploaded(p.Oid)
	}

	q.Wait()
	git.Logger.Printf("upload: Upload done\n");

	for _, err := range q.Errors() {
		if Debugging || lfs.IsFatalError(err) {
			LoggedError(err, err.Error())
		} else {
			if inner := lfs.GetInnerError(err); inner != nil {
				Error(inner.Error())
			}
			Error(err.Error())
		}
	}

	if len(q.Errors()) > 0 {
		os.Exit(2)
	}
}
