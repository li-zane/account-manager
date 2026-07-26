UPDATE mailbox_formats
SET enabled = FALSE, updated_at = now(), version = version + 1
WHERE id IN ('fmt_builtin_registered6', 'fmt_builtin_simple3', 'fmt_builtin_cf_routed3');

UPDATE mailbox_formats
SET name = '平台取件格式', updated_at = now(), version = version + 1
WHERE id = 'fmt_builtin_pickup2';

UPDATE mailbox_formats
SET name = 'Outlook 邮箱凭证', updated_at = now(), version = version + 1
WHERE id = 'fmt_builtin_outlook4';
