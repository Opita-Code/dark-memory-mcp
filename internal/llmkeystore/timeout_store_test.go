// timeout_store_test.go — el keyring NUNCA cuelga el boot.
//
// MEDIDO 2026-11-17 en esta maquina: con la coleccion del Secret Service
// BLOQUEADA y sin agente de prompts en la sesion, `ring.Set(...)` NO VUELVE
// NUNCA. Antes de este fix, esa llamada estaba en el BOOT del servidor MCP
// (antes de `ServeStdio`), y el harness veia "mcp connect failed ... Request
// timed out" a los ~34 s: el MCP entero sin servir.
//
// Estos tests NO SIMULAN el cuelgue con un store de mentira cuando pueden
// usar el mecanismo real: un store cuyo metodo se bloquea es exactamente el
// mismo contrato que el keyring bloqueado cumple (la llamada no vuelve).

package llmkeystore

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// blockingStore se bloquea para siempre en las operaciones que se le pidan.
//
// Es el contrato exacto del keyring bloqueado: la llamada no devuelve.
type blockingStore struct {
	blockSet      bool
	blockGet      bool
	blockHas      bool
	blockDelete   bool
	blockStoreCre bool
	blockLoadCre  bool
	unblock       chan struct{}
	once          sync.Once
}

func newBlockingStore() *blockingStore {
	return &blockingStore{unblock: make(chan struct{})}
}

func (b *blockingStore) wait() {
	<-b.unblock
}

func (b *blockingStore) release() {
	b.once.Do(func() { close(b.unblock) })
}

func (b *blockingStore) Get(providerID string) (string, error) {
	if b.blockGet {
		b.wait()
	}
	return "", ErrKeyNotFound
}

func (b *blockingStore) Set(providerID, key string) error {
	if b.blockSet {
		b.wait()
	}
	return nil
}

func (b *blockingStore) Delete(providerID string) error {
	if b.blockDelete {
		b.wait()
	}
	return nil
}

func (b *blockingStore) Has(providerID string) bool {
	if b.blockHas {
		b.wait()
	}
	return false
}

func (b *blockingStore) Source(providerID string) string { return "keyring" }

func (b *blockingStore) StoreCredential(providerID string, c Credential) error {
	if b.blockStoreCre {
		b.wait()
	}
	return nil
}

func (b *blockingStore) LoadCredential(providerID string) (Credential, bool) {
	if b.blockLoadCre {
		b.wait()
	}
	return Credential{}, false
}

// ── el test que importa: la escritura NO cuelga ──────────────────────────

func TestTimeoutStore_SetQueBloquea_ReturnsEnElPlazo(t *testing.T) {
	// ESTE ES EL BUG, en forma de test: una escritura que no vuelve.
	//
	// Antes del fix, esta llamada bloqueaba el boot del MCP para siempre.
	blocker := newBlockingStore()
	blocker.blockSet = true
	defer blocker.release()

	store := NewTimeoutStoreWith(blocker, 100*time.Millisecond)

	start := time.Now()
	err := store.Set("minimax", "sk-test")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Set deberia devolver error tras el plazo, no devolver nil")
	}
	if !errors.Is(err, ErrKeyringTimeout) {
		t.Fatalf("Set deberia devolver ErrKeyringTimeout, devolvio %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Set tardo %v: el plazo no se respeta", elapsed)
	}
	t.Logf("Set abortado en %v (plazo 100ms)", elapsed)
}

func TestTimeoutStore_GetQueBloquea_ReturnsEnElPlazo(t *testing.T) {
	blocker := newBlockingStore()
	blocker.blockGet = true
	defer blocker.release()

	store := NewTimeoutStoreWith(blocker, 100*time.Millisecond)

	start := time.Now()
	_, err := store.Get("minimax")
	elapsed := time.Since(start)

	if !errors.Is(err, ErrKeyringTimeout) {
		t.Fatalf("Get deberia devolver ErrKeyringTimeout, devolvio %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Get tardo %v: el plazo no se respeta", elapsed)
	}
	t.Logf("Get abortado en %v (plazo 100ms)", elapsed)
}

// ── la degradacion: el composite cae al backend de env ───────────────────

func TestComposite_CaeAlEnvCuandoElKeyringSeCuelga(t *testing.T) {
	// El COMPOSITE es lo que el boot construye: keyring + env.
	// Con el keyring colgado, la LECTURA tiene que caer al env: si no, el
	// juez no ve ninguna clave y el veredicto sale "needs_human" con
	// confianza 0 (ausencia de veredicto, no reprobacion).
	blocker := newBlockingStore()
	blocker.blockGet = true
	defer blocker.release()

	env := EnvStore(func(providerID string) string {
		return "DARK_TEST_KEY_" + providerID
	})
	t.Setenv("DARK_TEST_KEY_minimax", "sk-desde-env")

	safe := NewTimeoutStoreWith(blocker, 100*time.Millisecond)
	composite := NewComposite(safe, env)

	got, err := composite.Get("minimax")
	if err != nil {
		t.Fatalf("la lectura deberia caer al env, devolvio error %v", err)
	}
	if got != "sk-desde-env" {
		t.Fatalf("la lectura deberia devolver la clave del env, devolvio %q", got)
	}
}

// ── el caso bueno: un keyring sano NO se degrada ─────────────────────────

func TestTimeoutStore_UnStoreSano_NoSeDegrada(t *testing.T) {
	// Un store que responde bien dentro del plazo tiene que devolver SU
	// resultado, no un timeout. El plazo no puede Convertir un keyring sano
	// en uno roto.
	// Se usa un store EN MEMORIA (escribible), no el envStore: el env es de
	// SOLO LECTURA por diseno (lo posee el harness), y su Set falla a
	// proposito — que seria un test de otra cosa.
	mem := NewMemoryStore()
	mem.Set("minimax", "sk-real")

	store := NewTimeoutStoreWith(mem, 2*time.Second)

	got, err := store.Get("minimax")
	if err != nil {
		t.Fatalf("Get sobre un store sano devolvio error %v", err)
	}
	if got != "sk-real" {
		t.Fatalf("Get devolvio %q, se esperaba %q", got, "sk-real")
	}
	if err := store.Set("minimax", "sk-nuevo"); err != nil {
		t.Fatalf("Set sobre un store sano devolvio error %v", err)
	}
	if !store.Has("minimax") {
		t.Fatal("Has deberia ser true tras un Set correcto")
	}
	// La escritura llego al backend, no se quedo en el store con timeout.
	back, _ := mem.Get("minimax")
	if back != "sk-nuevo" {
		t.Fatalf("el backend guardo %q, se esperaba %q", back, "sk-nuevo")
	}
}

// ── el plazo por defecto es holgado para un keyring sano ─────────────────

func TestTimeoutStore_PlazoPorDefecto_EsHolgado(t *testing.T) {
	// MEDIDO 2026-11-17: `keyring.Open` contra un Secret Service sano
	// tarda entre 0,8 ms y 8,7 ms. El plazo por defecto (2 s) es tres
	// ordenes de magnitud por encima: un keyring sano NUNCA va a
	// falsos-positivar.
	if DefaultOperationTimeout < 500*time.Millisecond {
		t.Fatalf("el plazo por defecto (%v) es demasiado corto para un keyring sano", DefaultOperationTimeout)
	}
	if DefaultOperationTimeout > 10*time.Second {
		t.Fatalf("el plazo por defecto (%v) es demasiado largo: el harness espera 34 s", DefaultOperationTimeout)
	}
	t.Logf("plazo por defecto: %v", DefaultOperationTimeout)
}

// ── la migracion abandonada NO rompe nada ────────────────────────────────

func TestMigrateFromEnv_ConKeyringColgado_NoSeColga(t *testing.T) {
	// La migracion env→keyring ESCRIBE. Con el keyring bloqueado, se
	// quedaba esperando para siempre, en el BOOT.
	blocker := newBlockingStore()
	blocker.blockSet = true
	defer blocker.release()

	safe := NewTimeoutStoreWith(blocker, 100*time.Millisecond)
	entries := EnvEntriesFromCatalog([][2]string{{"minimax", "DARK_TEST_KEY_minimax"}})
	t.Setenv("DARK_TEST_KEY_minimax", "sk-desde-env")

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := MigrateFromEnv(safe, entries)
		if err == nil {
			t.Error("la migracion deberia fallar contra un keyring colgado")
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("la migracion se quedo colgada: el boot habria colgado")
	}
}
