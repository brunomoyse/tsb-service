<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    /**
     * Run the migrations.
     */
    public function up(): void
    {
        Schema::table('product_category_translations', function (Blueprint $table) {
            $table->dropUnique('product_category_translations_product_category_id_language_unique');
            $table->dropColumn('language');
            $table->enum('locale', ['EN', 'FR']);
            $table->unique(['product_category_id', 'locale']);
        });

        Schema::table('product_translations', function (Blueprint $table) {
            $table->dropUnique('product_translations_product_id_language_unique');
            $table->dropColumn('language');
            $table->enum('locale', ['EN', 'FR']);
            $table->unique(['product_id', 'locale']);
        });
    }

    /**
     * Reverse the migrations.
     */
    public function down(): void
    {
        //
    }
};
