package orchestration

import (
	"crypto/sha256"
	"strings"
)

// promptVersionOf returns the hex SHA-256 of the QUESTION that produced a
// verdict, or "" when no question was asked.
//
// WHY THIS EXISTS. The `prompt_version` column has been in the schema since
// v29 and was never filled: a stored verdict said WHAT was judged
// (artifact_sha256) and WHO judged it (model, nli_provider_id), but not WHAT
// WAS ASKED. That is not a cosmetic gap. The drift judge answers a QUESTION
// about an artifact, and the same artifact with a different question can get
// a different verdict — measured in the literature at 21.2% of individual
// verdicts flipping on a judge rerun and 28.2 percentage points more when the
// instructions change. A reader that only has the artifact digest cannot
// tell whether a stored verdict still answers the question it was asked.
//
// With the digest stored, a reader CAN tell: same artifact + same question =
// the same judgement; same artifact + different question = a different
// judgement, and the old one no longer counts as current. That is the
// "vigente" rule this repo already applies to the artifact sha, extended to
// the question.
//
// THE EMPTY CASE IS NOT AN EMPTY QUESTION. A blank spec_intent means "no
// question was asked" (the legacy content path, and every row written
// before this change). It returns "" so the column stays NULL, and NULL
// means exactly that: the question was not recorded. A digest of the empty
// string would be a lie — it would say "this verdict answers the empty
// question" and would look like a recorded question to every reader.
//
// THE DIGEST, NOT THE QUESTION. The question itself is prose that can be
// long and can carry anything the caller put in it. What the store needs is
// an identity: two rows with the same digest were asked the same thing (the
// same bytes, so the same meaning), and two rows with different digests were
// not. sha256 is the same primitive the artifact digest already uses, so
// there is one identity function in the store and not two.
//
// WHERE IT IS FILLED. Every row that a question produced: the single-shot
// drift judge (the path the repo's own judge runner uses), the N-shot
// drift consensus rows, the generic judge row and the generic consensus row.
// A row written without a question keeps prompt_version NULL.
func promptVersionOf(specIntent string) string {
	if strings.TrimSpace(specIntent) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(specIntent))
	return hexBytes(sum)
}
