package document

import (
	"context"

	"gostalgia/internal/vfs"
)

// Store coordinates document management, recents, favorites, associations,
// permission-aware search, and open-with handoffs.
type Store struct {
	vfs          vfs.DocumentFS
	grants       *vfs.GrantStore
	recents      *RecentsStore
	favorites    *FavoritesStore
	associations *AssociationTable
	searcher     *Searcher
	handoff      *HandoffManager
}

// NewStore initializes a document management store with all subsystems.
func NewStore(
	dfs vfs.DocumentFS,
	grants *vfs.GrantStore,
	launcher AppLauncher,
	dispatcher IPCDispatcher,
) *Store {
	assoc := NewAssociationTable()
	recents := NewRecentsStore(dfs, grants, DefaultRecentsPath)
	favorites := NewFavoritesStore(dfs, grants, DefaultFavoritesPath)
	searcher := NewSearcher(dfs, grants, recents, favorites)
	handoff := NewHandoffManager(dfs, grants, assoc, recents, launcher, dispatcher)

	return &Store{
		vfs:          dfs,
		grants:       grants,
		recents:      recents,
		favorites:    favorites,
		associations: assoc,
		searcher:     searcher,
		handoff:      handoff,
	}
}

// Init loads persistent recents and favorites from VFS and indexes initial documents.
func (s *Store) Init(ctx context.Context) error {
	_ = s.recents.Load()
	_ = s.favorites.Load()
	_ = s.searcher.RebuildIndex(ctx)
	return nil
}

// SetLifetimeContext propagates the runtime lifetime context to subsystems such as HandoffManager.
func (s *Store) SetLifetimeContext(ctx context.Context) {
	if s.handoff != nil {
		s.handoff.SetLifetimeContext(ctx)
	}
}

// Recents returns the recents store.
func (s *Store) Recents() *RecentsStore {
	return s.recents
}

// Favorites returns the favorites store.
func (s *Store) Favorites() *FavoritesStore {
	return s.favorites
}

// Associations returns the document type association table.
func (s *Store) Associations() *AssociationTable {
	return s.associations
}

// Searcher returns the permission-aware searcher and index.
func (s *Store) Searcher() *Searcher {
	return s.searcher
}

// Handoff returns the open-with handoff manager.
func (s *Store) Handoff() *HandoffManager {
	return s.handoff
}
