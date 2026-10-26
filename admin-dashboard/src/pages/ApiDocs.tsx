import { useQuery } from "@tanstack/react-query";
import { ApiReferenceReact } from "@scalar/api-reference-react";
import "@scalar/api-reference-react/style.css";
import { ErrorAlert } from "@/components/common";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api";
import { useAppId } from "@/lib/hooks";
import { useTheme } from "@/lib/theme";

export default function ApiDocs() {
  const app = useAppId();
  const { theme } = useTheme();
  const spec = useQuery({
    queryKey: ["app", app, "openapi"],
    queryFn: () => api.openapi(app),
  });

  if (spec.isLoading) {
    return (
      <div className="space-y-4 p-4">
        <Skeleton className="h-10 w-1/3" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (spec.isError) {
    return <ErrorAlert error={spec.error} title="Failed to load OpenAPI documentation" />;
  }

  return (
    <div className="overflow-hidden rounded-xl border bg-card">
      <ApiReferenceReact
        configuration={{
          spec: {
            content: spec.data,
          },
          theme: theme === "dark" ? "moon" : "default",
          darkMode: theme === "dark",
          hideModels: false,
        }}
      />
    </div>
  );
}
