package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	sdk "github.com/apteva/app-sdk"
)

const tenantLogLimit int64 = 10 << 20

// Keep the child's inherited file descriptor valid across Fleet restarts.
// Copy/truncate is deliberate: renaming would leave surviving children writing
// indefinitely into the old inode. Rotation retains five bounded log tails.
func rotateTenantLog(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() <= tenantLogLimit {
		return nil
	}
	for i := 4; i >= 1; i-- {
		if err = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	out, err := os.OpenFile(path+".1", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.NewSectionReader(f, info.Size()-tenantLogLimit, tenantLogLimit))
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return f.Truncate(0)
}

func (a *App) runLogRetention(ctx context.Context, app *sdk.AppCtx) error {
	tenants, err := a.store.list(map[string]string{})
	if err != nil {
		return err
	}
	for _, t := range tenants {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if t.Kind != KindLocal {
			continue
		}
		done, lockErr := a.beginTenantOperation(t.ID, "log rotation")
		if lockErr != nil {
			continue
		}
		if t.IsHosted() {
			script := `import os,sys
p=sys.argv[1]
try:
 f=open(p,'r+b',buffering=0)
except FileNotFoundError:
 sys.exit(0)
with f:
 size=os.fstat(f.fileno()).st_size
 if size>10485760:
  for i in range(4,0,-1):
   try: os.replace(p+'.'+str(i),p+'.'+str(i+1))
   except FileNotFoundError: pass
  f.seek(size-10485760)
  with open(p+'.1','wb') as out: out.write(f.read(10485760))
  f.truncate(0)
`
			_, code, runErr := instanceRunCommand(app, t.InstanceID, "python3 - "+sh(t.ConfigDir+"/fleet-child.log")+" <<'FLEET_LOG'\n"+script+"\nFLEET_LOG", 30)
			if runErr != nil || code != 0 {
				app.Logger().Warn("fleet log rotation failed", "tenant", t.ID, "error", runErr, "exit", code)
			}
		} else if validateLocalTenantDir(t.Slug, t.ConfigDir) == nil {
			if err := rotateTenantLog(filepath.Join(t.ConfigDir, "fleet-child.log")); err != nil {
				app.Logger().Warn("fleet log rotation failed", "tenant", t.ID, "error", err)
			}
		}
		done()
	}
	return nil
}
