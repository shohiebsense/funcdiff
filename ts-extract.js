#!/usr/bin/env node
// ts-extract.js — pure JS version using TypeScript compiler API

const ts = require("typescript");

async function main() {
  try {
    const fileName = process.argv[2] || "file.ts";
    const sourceText = await readStdin();

    const sourceFile = ts.createSourceFile(
      fileName,
      sourceText,
      ts.ScriptTarget.Latest,
      true,
      ts.ScriptKind.TS
    );

    const methods = [];

    function getDecoratorNames(node) {
      const ds = node.decorators;
      if (!ds) return [];
      const names = [];
      ds.forEach((d) => {
        const e = d.expression;
        if (ts.isCallExpression(e)) {
          if (ts.isIdentifier(e.expression)) names.push(e.expression.text);
        } else if (ts.isIdentifier(e)) {
          names.push(e.text);
        }
      });
      return names;
    }

    function buildSignature(sf, m) {
      const printer = ts.createPrinter({ removeComments: true });
      const params = m.parameters
        .map((p) => printer.printNode(ts.EmitHint.Unspecified, p, sf))
        .join(", ");
      let ret = "";
      if (m.type) {
        ret = printer.printNode(ts.EmitHint.Unspecified, m.type, sf);
      }
      return ret ? `(${params}) => ${ret}` : `(${params})`;
    }

    function visit(node) {
      if (ts.isClassDeclaration(node) && node.name) {
        const className = node.name.text;
        const classDecorators = getDecoratorNames(node);
        const isController = classDecorators.includes("Controller");
        const isInjectable = classDecorators.includes("Injectable");

        node.members.forEach((member) => {
          if (!ts.isMethodDeclaration(member) || !member.name) return;
          const nameNode = member.name;
          if (!ts.isIdentifier(nameNode)) return; // skip computed names like ['x']

          const methodName = nameNode.text;

          const start = sourceFile.getLineAndCharacterOfPosition(
            member.getStart()
          );
          const end = sourceFile.getLineAndCharacterOfPosition(member.getEnd());
          const startLine = start.line + 1;
          const endLine = end.line + 1;
          const lineCount = endLine - startLine + 1;

          const signature = buildSignature(sourceFile, member);
          const exported = (node.modifiers || []).some(
            (m) => m.kind === ts.SyntaxKind.ExportKeyword
          );

          let kind = "function";
          if (isController) kind = "controller";
          else if (isInjectable) kind = "service";

          methods.push({
            kind,
            className,
            methodName,
            signature,
            exported,
            startLine,
            endLine,
            lineCount,
          });
        });
      }

      ts.forEachChild(node, visit);
    }

    visit(sourceFile);

    process.stdout.write(JSON.stringify(methods));
  } catch (err) {
    console.error(
      `ts-extract error: ${err && err.stack ? err.stack : String(err)}`
    );
    process.exit(1);
  }
}

function readStdin() {
  return new Promise((resolve, reject) => {
    let data = "";
    process.stdin.setEncoding("utf8");
    process.stdin.on("data", (chunk) => (data += chunk));
    process.stdin.on("end", () => resolve(data));
    process.stdin.on("error", reject);
  });
}

main();
