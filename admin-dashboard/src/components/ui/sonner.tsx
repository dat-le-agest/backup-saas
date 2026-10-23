import { Toaster as Sonner, type ToasterProps } from "sonner";
import { useTheme } from "@/lib/theme";

function Toaster(props: ToasterProps) {
  const { theme } = useTheme();
  return <Sonner theme={theme} richColors closeButton className="toaster group" {...props} />;
}

export { Toaster };
