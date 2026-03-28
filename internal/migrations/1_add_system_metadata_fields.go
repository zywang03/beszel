package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

func init() {
	m.Register(func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId("systems")
		if err != nil {
			return err
		}

		if collection.Fields.GetByName("device_admin") == nil {
			collection.Fields.Add(&core.TextField{Name: "device_admin"})
		}
		if collection.Fields.GetByName("location") == nil {
			collection.Fields.Add(&core.TextField{Name: "location"})
		}

		return app.Save(collection)
	}, nil)
}
