# Upgrading shortlink

Back up the database before you upgrade. Migrations run automatically when the server starts, and a migration can change the database in ways you cannot undo; the backup is what you go back to.

## Before you start

Don't copy the SQLite file directly. Back it up with the SQLite backup command while the server runs (see deploy.md):

```sh
sqlite3 shortlink.db ".backup backup.db"
```

Copying the file with `cp` can catch it partway through a write. If shortlink runs in the container image shortlink/shortlink, run the backup inside the container against the database in the /data volume.

## Step by step

1. Back up the database:

   ```sh
   sqlite3 shortlink.db ".backup backup.db"
   ```

2. If you are upgrading from v1.2.0 or earlier, run the new binary's migrate command first (see below).

3. Replace the binary with the new one, or pull the new container image.

4. Restart the server with `shortlink serve`.

5. Migrations run at startup. Confirm the new version with `shortlink version`.

## Upgrading from v1.2.0 or earlier

Automatic migrations arrived in v1.3.0. A database written by v1.2.0 or earlier predates them, so run the migrate command with the new binary before you start the server:

```sh
shortlink migrate
```

Then start the server as usual. `shortlink migrate` applies the migrations and exits; it is safe to run on any version, since a database that is already current has nothing left to apply.

## After the upgrade

Run `shortlink check-config` with the new binary to confirm the configuration still loads. New releases may add settings with defaults (see config/defaults.yaml), so a configuration file from an older version keeps working without edits.

## Downgrading

Downgrading after a migration is not supported. Older binaries do not know about tables or columns added by newer migrations, and there is no command that removes them. To go back to an older version, stop the server and restore the backup you took before upgrading:

```sh
sqlite3 shortlink.db ".restore backup.db"
```

Then run the older binary against the restored file. Keep the backup until you are sure the new version is working.
