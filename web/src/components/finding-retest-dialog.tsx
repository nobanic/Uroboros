"use client";

import { FindingDialogHeading } from "@/astra/finding-inspector";

import * as React from "react";

import { RotateCcwIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter } from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { FindingRetest } from "@/lib/types";

interface FindingRetestDialogProps {
  findingId: string;
  findingName?: string;
  onClose: () => void;
  onStarted?: (retest: FindingRetest) => void;
}

// Only mounted while open; the notes are cleared after close. The list and detail views share the submit lock and error handling, and stay on the current page after starting.
export function FindingRetestDialog({ findingId, findingName, onClose, onStarted }: FindingRetestDialogProps) {
  const notesId = React.useId();
  const [notes, setNotes] = React.useState("");
  const [submitting, setSubmitting] = React.useState(false);
  const submitLock = React.useRef(false);

  async function start() {
    if (submitLock.current) return;
    submitLock.current = true;
    setSubmitting(true);
    try {
      const result = await api.startFindingRetest(findingId, notes.trim());
      onStarted?.(result.retest);
      onClose();
      toast.success(result.created ? "Retest started; click \"Retesting\" to view the session" : "This vulnerability is already being retested; you can view the existing session");
    } catch (e) {
      toast.error(`Failed to start retest: ${(e as Error).message}`);
    } finally {
      submitLock.current = false;
      setSubmitting(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && !submitLock.current && onClose()}>
      <DialogContent>
        <FindingDialogHeading
          title={<>Retest vulnerability #{findingId}</>}
          description={
            <>
              {findingName ? <span className="mb-2 block">{findingName}</span> : null}
              The retest Agent reads the original evidence and testing constraints and runs targeted verification in a
              separate session. Once the retest completes successfully and the fix is confirmed, the vulnerability status
              is automatically changed to "Fixed"; other verdicts keep their original status.
            </>
          }
          descriptionClassName="break-words"
        />
        <FieldGroup>
          <Field data-disabled={submitting}>
            <FieldLabel htmlFor={notesId}>Additional notes (optional)</FieldLabel>
            <Textarea
              id={notesId}
              value={notes}
              maxLength={4000}
              rows={4}
              disabled={submitting}
              onChange={(e) => setNotes(e.target.value)}
              placeholder="e.g. verify the original endpoint with the original test account; fixed in version v2."
            />
            <FieldDescription>
              You can add the fix version, testing conditions, or limitations for this retest.
            </FieldDescription>
          </Field>
        </FieldGroup>
        <DialogFooter>
          <Button variant="outline" disabled={submitting} onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={submitting} onClick={() => void start()}>
            {submitting ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}
            {submitting ? "Creating…" : "Start retest"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
