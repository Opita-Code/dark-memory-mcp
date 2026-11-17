// timeout_store.go — un key store que NUNCA se cuelga.
//
// EL PROBLEMA, MEDIDO
//
// MEDIDO 2026-11-17 en esta maquina: el keyring del SO (Secret Service de
// dbus, via 99designs/keyring) puede quedar con la coleccion BLOQUEADA y sin
// agente de prompts en la sesion. Medido con una sonda propia:
//
//   keyring.Open  -> 8 ms   (FUNCIONA: solo abre la conexion)
//   ring.Set(...) -> COLGADO > 15 s, sin volver nunca
//
// El `Unlock` del Secret Service devuelve un prompt
// (`/org/freedesktop/secrets/prompt/u82`) que se AUTODESTRUYE si nadie lo
// atiende: en una sesion sin agente de prompts no hay forma de desbloquear.
//
// Y la lectura por dbus de una coleccion bloqueada tambien puede quedarse
// esperando el prompt de desbloqueo.
//
// POR QUE ESTE STORE EXISTE
//
// `DefaultKeyring()` se llama en el BOOT del servidor MCP, ANTES de
// `ServeStdio`. Con el keyring colgado, el boot no llegaba nunca a servir y
// el harness veia "mcp connect failed ... Request timed out" a los ~34 s.
//
// El keyring es una DEPENDENCIA NO CRITICA del serving: el MCP sirve sus 78
// tools sin ninguna credencial de LLM. Que el boot NO SIRVA porque un vault
// del sistema esta colgado es un fallo de separacion de criticidad.
//
// QUE HACE ESTE STORE
//
// Envuelve cualquier KeyStore y le pone un DEADLINE a cada operacion:
//
//   - Si la operacion responde en plazo, su resultado vale.
//   - Si NO responde en plazo, se abandona y se degrada: las LECTURAS caen
//     al backend siguiente (o a las variables de entorno), y las ESCRITURAS
//     devuelven un error de timeout en vez de colgarse.
//
// El abandono no mata la goroutine: la libreria de dbus no tiene forma de
// cancelar una llamada a medias, asi que la goroutine se queda huerfana
// esperando. Se acepta ese coste (una goroutine por operacion que expira)
// a cambio de que el PROCESO nunca se cuelgue: un boot colgado deja el MCP
// entero sin servir, y una goroutine huerfana no.
//
// POR QUE NO UN CONTEXT
//
// La interfaz KeyStore no lleva `context.Context`, y la libreria del keyring
// (99designs/keyring v1.2.2) no lo acepta en sus metodos. Un timeout
// por goroutine es la unica forma de acotarlos sin cambiar la libreria.

package llmkeystore

import (
	"errors"
	"time"
)

// DefaultOperationTimeout es el plazo por operacion del keyring.
//
// # POR QUE 2 SEGUNDOS
//
// MEDIDO 2026-11-17: `keyring.Open` contra un Secret Service sano tarda
// entre 0,8 ms y 8,7 ms. O sea, un plazo de 2 s es tres ordenes de magnitud
// por encima del caso bueno: no va a falsos-positivivar un keyring sano.
//
// Y contra un keyring colgado, 2 s es la diferencia entre "el MCP arranca" y
// "el harness espera 34 s y se rinde".
var DefaultOperationTimeout = 2 * time.Second

// ErrKeyringTimeout es el error que devuelven las operaciones que
// superaron el deadline. Es distinguible de los demas errores del paquete
// para que quien llama pueda decidir degradar en vez de fallar.
var ErrKeyringTimeout = errors.New("llm keystore: operation timed out")

// TimeoutStore envuelve un KeyStore con un deadline por operacion.
//
// Las escrituras que expiran NO se reintentan: el keyring sigue colgado, y
// reintentar solo multiplica las goroutine huerfanas.
type TimeoutStore struct {
	inner   KeyStore
	timeout time.Duration
}

// NewTimeoutStore wraps inner with the default deadline.
func NewTimeoutStore(inner KeyStore) *TimeoutStore {
	return &TimeoutStore{inner: inner, timeout: DefaultOperationTimeout}
}

// NewTimeoutStoreWith wraps inner with an explicit deadline.
func NewTimeoutStoreWith(inner KeyStore, timeout time.Duration) *TimeoutStore {
	if timeout <= 0 {
		timeout = DefaultOperationTimeout
	}
	return &TimeoutStore{inner: inner, timeout: timeout}
}

// run executes fn under the deadline.
//
// NO es un metodo: Go no permite metodos genericos, asi que es una funcion
// libre que recibe el plazo.
//
// La goroutine abandonada escribe su resultado en un canal con buffer 1: si
// la operacion termina DESPUES del plazo, el envio no se bloquea (el buffer
// lo absorbe) y la goroutine se puede garbage-collectear.
func runWithDeadline[T any](timeout time.Duration, fn func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)

	go func() {
		v, err := fn()
		ch <- result{v: v, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case r := <-ch:
		return r.v, r.err
	case <-timer.C:
		var zero T
		return zero, ErrKeyringTimeout
	}
}

// Get reads a provider key under the deadline.
func (t *TimeoutStore) Get(providerID string) (string, error) {
	return runWithDeadline(t.timeout, func() (string, error) {
		return t.inner.Get(providerID)
	})
}

// Set writes a provider key under the deadline.
func (t *TimeoutStore) Set(providerID, key string) error {
	_, err := runWithDeadline(t.timeout, func() (struct{}, error) {
		return struct{}{}, t.inner.Set(providerID, key)
	})
	return err
}

// Delete removes a provider key under the deadline.
func (t *TimeoutStore) Delete(providerID string) error {
	_, err := runWithDeadline(t.timeout, func() (struct{}, error) {
		return struct{}{}, t.inner.Delete(providerID)
	})
	return err
}

// Has reports whether a provider key exists, under the deadline.
//
// Un timeout aqui NO es "no existe": es "no se pudo saber". Pero la
// firma es booleana, asi que un timeout se reporta como `false` y quien
// llama acaba preguntando a otro backend (el composite recorre la cadena).
func (t *TimeoutStore) Has(providerID string) bool {
	v, err := runWithDeadline(t.timeout, func() (bool, error) {
		return t.inner.Has(providerID), nil
	})
	if err != nil {
		return false
	}
	return v
}

// Source reports where the key came from ("keyring", "env", ...).
func (t *TimeoutStore) Source(providerID string) string {
	v, err := runWithDeadline(t.timeout, func() (string, error) {
		return t.inner.Source(providerID), nil
	})
	if err != nil {
		return ""
	}
	return v
}

// StoreCredential persists key+endpoint under the deadline.
//
// El backend puede no implementar la extension de credenciales (el backend
// de env, por ejemplo): en ese caso la operacion no tiene sentido y se
// reporta como "no almacenado" sin error, igual que hace el envStore.
func (t *TimeoutStore) StoreCredential(providerID string, c Credential) error {
	cs, ok := t.inner.(CredentialStore)
	if !ok {
		return nil
	}
	_, err := runWithDeadline(t.timeout, func() (struct{}, error) {
		return struct{}{}, cs.StoreCredential(providerID, c)
	})
	return err
}

// LoadCredential reads a credential record under the deadline.
//
// Si el backend no implementa la extension, o si la operacion expira, el
// bool es false: "no hay credencial conocida". El composite sigue la
// cadena hacia el siguiente backend.
func (t *TimeoutStore) LoadCredential(providerID string) (Credential, bool) {
	cs, ok := t.inner.(CredentialStore)
	if !ok {
		return Credential{}, false
	}
	type credResult struct {
		c  Credential
		ok bool
	}
	ch := make(chan credResult, 1)
	go func() {
		c, found := cs.LoadCredential(providerID)
		ch <- credResult{c: c, ok: found}
	}()

	timer := time.NewTimer(t.timeout)
	defer timer.Stop()

	select {
	case r := <-ch:
		return r.c, r.ok
	case <-timer.C:
		return Credential{}, false
	}
}

// compile-time check: TimeoutStore satisfies the full Store contract.
var _ Store = (*TimeoutStore)(nil)
