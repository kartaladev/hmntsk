import AttachFileIcon from "@mui/icons-material/AttachFile";
import SendIcon from "@mui/icons-material/Send";
import UploadFileIcon from "@mui/icons-material/UploadFile";
import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Stack from "@mui/material/Stack";
import Typography from "@mui/material/Typography";
import { useEffect, useState } from "react";

import { api, type Issued, type Supplier, type Task } from "./api";
import { formatAmount, formatBytes } from "./format";
import { checkUpload, registryEntry, uploadAccept } from "./purchaseOrder";

// The forms the purchase order task types name in their hmntsk.formKey. They
// are the application's own, not rendered from a schema: sending and
// uploading are done through the application's API, which stores the document
// and completes the task in one transaction.
export const sendOrderForm = "send-purchase-order-form";
export const uploadOrderForm = "upload-purchase-order-form";

export function isPurchaseOrderForm(formKey: string | undefined): formKey is typeof sendOrderForm | typeof uploadOrderForm {
  return formKey === sendOrderForm || formKey === uploadOrderForm;
}

type Props = {
  task: Task;
  form: typeof sendOrderForm | typeof uploadOrderForm;
  onIssued: (issued: Issued) => void;
  // onError hands a failure back to the task panel, which knows how to show a
  // task that changed underneath it.
  onError: (error: unknown) => void | Promise<void>;
};

// PurchaseOrderForm is where purchasing issues an approved order's purchase
// order: sent, for a supplier on the registry, or uploaded, for anyone else.
export function PurchaseOrderForm({ task, form, onIssued, onError }: Props) {
  const orderId = task.correlation?.ownerRef ?? "";
  const supplier = String(task.input?.supplier ?? "");
  const [suppliers, setSuppliers] = useState<Supplier[]>();
  const [file, setFile] = useState<File>();
  const [fileProblem, setFileProblem] = useState<string>();
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (form === sendOrderForm) {
      api.suppliers().then(setSuppliers, () => setSuppliers([]));
    }
  }, [form]);

  const contact = suppliers && registryEntry(suppliers, supplier)?.contact;

  const issue = async () => {
    setBusy(true);

    try {
      onIssued(
        form === sendOrderForm
          ? await api.sendPurchaseOrder(orderId, task)
          : await api.uploadPurchaseOrder(orderId, task, file!),
      );
    } catch (e) {
      await onError(e);
    } finally {
      setBusy(false);
    }
  };

  const summary = (
    <Box component="dl" sx={{ display: "grid", gridTemplateColumns: "max-content 1fr", columnGap: 2, rowGap: 0.5, m: 0 }}>
      <Term label="Supplier" value={contact ? `${supplier} <${contact}>` : supplier} />
      <Term label="Item" value={String(task.input?.description ?? "")} />
      <Term label="Amount" value={formatAmount(Number(task.input?.amount ?? 0))} />
    </Box>
  );

  if (form === sendOrderForm) {
    return (
      <Stack spacing={2}>
        <Typography variant="subtitle1" sx={{ fontWeight: 700 }}>
          Send the purchase order
        </Typography>
        {summary}
        <Typography variant="body2" color="text.secondary">
          The application drafts the purchase order from the approved order and sends it to the supplier&apos;s
          contact on the registry. Their invoice follows.
        </Typography>
        {suppliers && !contact && (
          <Alert severity="warning">{supplier} is no longer on the supplier registry, so nothing can be sent.</Alert>
        )}
        <Stack direction="row" sx={{ justifyContent: "flex-end" }}>
          <Button variant="contained" startIcon={<SendIcon />} disabled={busy || !contact} onClick={() => void issue()}>
            Send purchase order
          </Button>
        </Stack>
      </Stack>
    );
  }

  return (
    <Stack spacing={2}>
      <Typography variant="subtitle1" sx={{ fontWeight: 700 }}>
        Upload the purchase order
      </Typography>
      {summary}
      <Alert severity="info">
        {supplier} is not on the supplier registry. Agree the order with them, then upload the signed purchase order: a
        PDF, PNG or JPEG of at most 5 MB.
      </Alert>
      <Stack direction="row" spacing={2} sx={{ alignItems: "center", flexWrap: "wrap" }}>
        <Button component="label" variant="outlined" startIcon={<AttachFileIcon />} disabled={busy}>
          Choose file
          <input
            type="file"
            hidden
            accept={uploadAccept}
            onChange={(e) => {
              const chosen = e.target.files?.[0];
              // Cleared so that choosing the same file again is still a change.
              e.target.value = "";

              if (chosen) {
                const problem = checkUpload(chosen);
                setFileProblem(problem);
                setFile(problem ? undefined : chosen);
              }
            }}
          />
        </Button>
        {file && (
          <Typography variant="body2">
            {file.name} · {formatBytes(file.size)}
          </Typography>
        )}
      </Stack>
      {fileProblem && (
        <Typography variant="body2" color="error" role="alert">
          {fileProblem}
        </Typography>
      )}
      <Stack direction="row" sx={{ justifyContent: "flex-end" }}>
        <Button variant="contained" startIcon={<UploadFileIcon />} disabled={busy || !file} onClick={() => void issue()}>
          Upload purchase order
        </Button>
      </Stack>
    </Stack>
  );
}

function Term({ label, value }: { label: string; value: string }) {
  return (
    <>
      <Typography component="dt" variant="body2" color="text.secondary">
        {label}
      </Typography>
      <Typography component="dd" variant="body2" sx={{ m: 0 }}>
        {value}
      </Typography>
    </>
  );
}
