package vmsnapshot

import (
	"errors"
	"regexp"
	"testing"

	"github.com/super-phenix/superphenix/internal/superphenix-api/internal/db"
	"github.com/super-phenix/superphenix/internal/superphenix-api/internal/db/model"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func setupMockDB(t *testing.T) (sqlmock.Sqlmock, func()) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}

	gormDB, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sqlDB,
	}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm: %v", err)
	}

	oldClient := db.Client
	db.Client = gormDB
	return mock, func() {
		db.Client = oldClient
		_ = sqlDB.Close()
	}
}

func TestRestoreInstanceInDb(t *testing.T) {
	id := uuid.New()
	projectId := uuid.New()
	otherProjectId := uuid.New()
	eid := "spx-restored-instance"
	selectQuery := regexp.QuoteMeta(`SELECT * FROM "products" WHERE id = $1`)
	columns := []string{"id", "effective_id", "product_name", "code_az", "project_id", "product_type_id"}

	tests := []struct {
		name       string
		mockSetup  func(mock sqlmock.Sqlmock)
		wantErr    error
		wantAnyErr bool
	}{
		{
			name: "creates the row when the instance is unknown",
			mockSetup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(selectQuery).WillReturnError(gorm.ErrRecordNotFound)
				mock.ExpectBegin()
				mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "products"`)).
					WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(id))
				mock.ExpectCommit()
			},
		},
		{
			name: "insert failure is returned and never turns into an update",
			mockSetup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(selectQuery).WillReturnError(gorm.ErrRecordNotFound)
				mock.ExpectBegin()
				mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "products"`)).
					WillReturnError(errors.New("duplicate key"))
				mock.ExpectRollback()
			},
			wantAnyErr: true,
		},
		{
			name: "undeletes the row of the same instance in the same project",
			mockSetup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(selectQuery).WillReturnRows(sqlmock.NewRows(columns).
					AddRow(id, eid, "old", "az1", projectId, model.ProductTypeInstance.Name))
				mock.ExpectBegin()
				mock.ExpectExec(regexp.QuoteMeta(`UPDATE "products" SET "deleted_at"=$1,"product_name"=$2,"updated_at"=$3 WHERE id = $4 AND project_id = $5`)).
					WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			},
		},
		{
			name: "rejects a row owned by another project",
			mockSetup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(selectQuery).WillReturnRows(sqlmock.NewRows(columns).
					AddRow(id, "spx-victim", "victim", "az1", otherProjectId, model.ProductTypeInstance.Name))
			},
			wantErr: errRestoreConflict,
		},
		{
			name: "rejects a row of another product type",
			mockSetup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(selectQuery).WillReturnRows(sqlmock.NewRows(columns).
					AddRow(id, eid, "disk", "az1", projectId, model.ProductTypeDisk.Name))
			},
			wantErr: errRestoreConflict,
		},
		{
			name: "rejects a row with another effective ID",
			mockSetup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(selectQuery).WillReturnRows(sqlmock.NewRows(columns).
					AddRow(id, "spx-other", "other", "az1", projectId, model.ProductTypeInstance.Name))
			},
			wantErr: errRestoreConflict,
		},
		{
			name: "returns lookup errors",
			mockSetup: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(selectQuery).WillReturnError(errors.New("db down"))
			},
			wantAnyErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, cleanup := setupMockDB(t)
			defer cleanup()
			tt.mockSetup(mock)

			err := restoreInstanceInDb(id, eid, "restored", "az1", projectId)

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
			case tt.wantAnyErr:
				if err == nil {
					t.Fatal("expected an error")
				}
			case err != nil:
				t.Fatalf("unexpected error: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations (an unexpected write may have happened): %v", err)
			}
		})
	}
}
