<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    /**
     * Run the migrations.
     */
    public function up(): void
    {
        Schema::table('attachments', function (Blueprint $table) {
            $table->boolean('preview')->default(false);
            // Other column definitions...
        });

        // Add a partial unique index using raw SQL
        DB::statement('CREATE UNIQUE INDEX attachments_preview_product_id_unique ON attachments (product_id) WHERE preview = true');
    }

    /**
     * Reverse the migrations.
     */
    public function down(): void
    {
        Schema::table('attachments', function (Blueprint $table) {
            $table->dropColumn('preview');
        });

        // Remove the partial unique index
        DB::statement('DROP INDEX IF EXISTS attachments_preview_product_id_unique ON attachments');
    }
};
