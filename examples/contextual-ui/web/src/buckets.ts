// A bucket is not something hmntsk stores. It is a query the host names, and
// `me` stands for whoever the server says is asking, which is what keeps these
// queries inside the default self-only policy.

export type Bucket = {
  name: string;
  label: string;
  params: [string, string][];
};

export const pageSize = 20;

export function buckets(now: Date): Bucket[] {
  return [
    {
      name: "available",
      label: "Available to claim",
      params: [
        ["candidate", "me"],
        ["status", "READY"],
      ],
    },
    {
      name: "mine",
      label: "Mine",
      params: [
        ["assignee", "me"],
        ["status", "RESERVED"],
        ["status", "IN_PROGRESS"],
      ],
    },
    {
      name: "overdue",
      label: "Overdue",
      params: [
        ["candidate", "me"],
        ["status", "READY"],
        ["status", "RESERVED"],
        ["status", "IN_PROGRESS"],
        ["dueBefore", now.toISOString()],
      ],
    },
  ];
}

// bucketQuery is the bucket's filter, for GET /v1/tasks/count.
export function bucketQuery(bucket: Bucket): string {
  return new URLSearchParams(bucket.params).toString();
}

// taskListQuery is a page of the bucket's tasks, most urgent first, for
// GET /v1/tasks. The cursor is the previous page's nextCursor, passed back as is.
export function taskListQuery(bucket: Bucket, cursor?: string): string {
  const params = new URLSearchParams(bucket.params);
  params.append("orderBy", "urgency");
  params.append("limit", String(pageSize));

  if (cursor) {
    params.append("cursor", cursor);
  }

  return params.toString();
}
