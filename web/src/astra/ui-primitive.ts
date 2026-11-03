import type { ElementType, JSX } from "react"
import { jsx } from "react/jsx-runtime"
import { cn } from "@/lib/utils"

// Each recipe retains its target, ordered attributes and class inputs. Callers'
// remaining props override recipe attributes exactly as in the original JSX.
export function uiPrimitive<Props extends { className?: string }>(
  name: string,
  target: ElementType,
  attributes: Readonly<Record<string, string>>,
  classes: readonly string[],
): (props: Props) => JSX.Element {
  function Primitive({ className, ...props }: Props): JSX.Element {
    return jsx(target, {
      ...attributes,
      className: cn(...classes, className),
      ...props,
    })
  }
  Object.defineProperty(Primitive, "name", { value: name, configurable: true })
  return Primitive
}
