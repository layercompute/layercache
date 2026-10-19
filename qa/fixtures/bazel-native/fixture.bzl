def _action_impl(ctx):
    output = ctx.actions.declare_file(ctx.label.name + ".txt")
    ctx.actions.run(
        executable = "/bin/sh",
        arguments = [ctx.file.tool.path, ctx.file.src.path, output.path],
        inputs = [ctx.file.src, ctx.file.tool],
        outputs = [output],
        mnemonic = "LayerCacheFixture",
        use_default_shell_env = True,
    )
    return [DefaultInfo(files = depset([output]))]

fixture_action = rule(
    implementation = _action_impl,
    attrs = {
        "src": attr.label(allow_single_file = True, mandatory = True),
        "tool": attr.label(allow_single_file = True, mandatory = True),
    },
)

def _test_impl(ctx):
    executable = ctx.actions.declare_file(ctx.label.name + ".sh")
    ctx.actions.write(executable, "#!/bin/sh\nprintf 'ran\\n' >> \"$FIXTURE_COUNTER\"\nexit 1\n", is_executable = True)
    return [DefaultInfo(executable = executable)]

fixture_test = rule(implementation = _test_impl, test = True)
