// Command relink-media moves the images rows still reference on the old media volume
// ("/media/..." or https://inventoryapi.../media/...) to the shared object storage and points each
// row at its new key: item images (with an item.updated event so catalogue consumers refresh
// their copy), gallery and variant images, uploaded category icons, brand and supplier logos.
// Files no row references are not moved; they go away with the volume.
//
// It reports counts and changes nothing unless run with --apply. It is safe to run again. Run it
// inside the inventory-api pod, where the media volume, the database and the object-storage
// Secret are available:
//
//	kubectl exec -n inventory deploy/inventory-api -- inventory-relink-media           # report
//	kubectl exec -n inventory deploy/inventory-api -- inventory-relink-media --apply   # move
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/Bengo-Hub/httpware/objectstore"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/zap"

	"github.com/bengobox/inventory-service/internal/ent"
	"github.com/bengobox/inventory-service/internal/modules/items"
)

func main() {
	apply := flag.Bool("apply", false, "move the files and update the rows (default reports only)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		log.Fatal("POSTGRES_URL is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	defer client.Close()

	store, err := objectstore.New("inventory", objectstore.FromEnv())
	if err != nil {
		log.Fatalf("object storage: %v", err)
	}
	if err := store.Check(ctx); err != nil {
		log.Fatalf("object storage: %v", err)
	}

	rep, err := items.NewService(client, zap.NewNop(), store).RelinkMedia(ctx, *apply)
	out, _ := json.MarshalIndent(rep, "", "  ")
	os.Stdout.Write(append(out, '\n'))
	if err != nil {
		log.Fatalf("relink stopped (run again to continue): %v", err)
	}
}
